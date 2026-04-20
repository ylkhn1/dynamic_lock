package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"dynamic-lock/internal/bluetooth"
	"dynamic-lock/internal/config"
	"dynamic-lock/internal/locker"
	"dynamic-lock/internal/sysinfo"
	"dynamic-lock/internal/telegram"
	"dynamic-lock/pkg/logger"
)

// promptDebounce prevents re-sending the unlock prompt on flapping Bluetooth.
const promptDebounce = 30 * time.Second

// Notifier is the minimal surface the main loop uses to push events outward
// (lock notification, unlock prompt). Implemented by *telegram.Bot or by
// noopNotifier when the Telegram integration is disabled.
type Notifier interface {
	Notify(text string)
	AskUnlock()
}

type noopNotifier struct{}

func (noopNotifier) Notify(string) {}
func (noopNotifier) AskUnlock()    {}

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file (.env or .yaml)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger.SetLevel(cfg.LogLevel)
	log := logger.Log

	log.WithFields(logrus.Fields{
		"mac":       cfg.DeviceMAC,
		"interval":  cfg.CheckInterval,
		"threshold": cfg.FailThreshold,
		"telegram":  cfg.TelegramBotToken != "",
	}).Info("dynamic-lock agent starting")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sigCh
		log.WithField("signal", s).Info("shutting down")
		cancel()
	}()

	svc := &agentService{
		mac:       cfg.DeviceMAC,
		startedAt: time.Now(),
	}

	var (
		wg                 sync.WaitGroup
		notifier Notifier = noopNotifier{}
	)

	if cfg.TelegramBotToken != "" {
		bot, err := telegram.New(telegram.Config{
			Token:         cfg.TelegramBotToken,
			AllowedUserID: cfg.TelegramAllowedUserID,
		}, svc, log)
		if err != nil {
			log.WithError(err).Error("telegram bot disabled")
		} else {
			notifier = bot
			wg.Add(1)
			go func() {
				defer wg.Done()
				bot.Run(ctx)
			}()
		}
	}

	run(ctx, cfg, log, notifier)
	wg.Wait()
	log.Info("dynamic-lock agent stopped")
}

// ---- service adapter --------------------------------------------------------

// agentService adapts package-level helpers to the telegram.Service interface.
// A future WebApp/REST layer can implement the same interface over richer
// internal state.
type agentService struct {
	mac       string
	startedAt time.Time
}

func (a *agentService) BluetoothStatus() (bool, error) {
	return bluetooth.CheckConnected(a.mac)
}

func (a *agentService) Lock() error                  { return locker.Lock() }
func (a *agentService) Unlock() error                { return locker.Unlock() }
func (a *agentService) Suspend() error               { return locker.Suspend() }
func (a *agentService) IsSessionLocked() (bool, error) { return locker.IsSessionLocked() }
func (a *agentService) Uptime() time.Duration        { return time.Since(a.startedAt) }
func (a *agentService) LogTail(n int) []string       { return logger.Tail(n) }
func (a *agentService) ExternalIP(ctx context.Context) (string, error) {
	return sysinfo.ExternalIP(ctx)
}

// ---- main loop --------------------------------------------------------------

func run(ctx context.Context, cfg *config.Config, log *logrus.Logger, notifier Notifier) {
	ticker := time.NewTicker(cfg.CheckInterval)
	defer ticker.Stop()

	var (
		failCount    int
		locked       bool
		seenBT       bool
		wasConnected bool
		lastPromptAt time.Time
	)

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			connected, err := bluetooth.CheckConnected(cfg.DeviceMAC)
			if err != nil {
				// Tool-level failure (timeout, binary missing) — skip this cycle
				// without counting it toward the debounce threshold.
				log.WithError(err).Warn("bluetooth check error, skipping cycle")
				continue
			}

			// Edge detection: disconnected -> connected.
			reconnected := seenBT && !wasConnected && connected
			seenBT = true
			wasConnected = connected

			if reconnected {
				handleReconnect(log, notifier, cfg.DeviceMAC, &lastPromptAt)
			}

			if connected {
				if failCount > 0 || locked {
					log.WithFields(logrus.Fields{
						"mac":        cfg.DeviceMAC,
						"was_locked": locked,
					}).Info("resetting fail counter")
					failCount = 0
					locked = false
				}
				continue
			}

			// Device not connected — advance the debounce counter.
			failCount++
			log.WithFields(logrus.Fields{
				"mac":        cfg.DeviceMAC,
				"fail_count": failCount,
				"threshold":  cfg.FailThreshold,
			}).Warn("device not connected")

			if failCount >= cfg.FailThreshold && !locked {
				// Double-check actual session state before locking. The user may
				// have locked manually (Ctrl+Alt+L, /lock via Telegram) while our
				// in-memory flag was still false — avoid a redundant loginctl
				// call and a misleading "locked" notification.
				if actual, err := locker.IsSessionLocked(); err == nil && actual {
					log.WithField("mac", cfg.DeviceMAC).Info("session already locked externally, syncing state")
					locked = true
					continue
				}

				log.WithField("mac", cfg.DeviceMAC).Warn("threshold reached, locking screen")
				if err := locker.Lock(); err != nil {
					log.WithError(err).Error("failed to lock screen")
				} else {
					log.Info("screen locked")
					locked = true
					go notifier.Notify("🔒 ПК заблокирован")
				}
			}
		}
	}
}

// handleReconnect fires when Bluetooth transitions disconnected → connected.
// It sends an unlock prompt to Telegram only if the session is actually locked
// and the debounce window has elapsed. No action is taken on the system here —
// unlocking requires the user to tap the confirmation button.
func handleReconnect(log *logrus.Logger, notifier Notifier, mac string, lastPromptAt *time.Time) {
	log.WithField("mac", mac).Info("bluetooth reconnected")

	if time.Since(*lastPromptAt) < promptDebounce {
		log.Debug("unlock prompt suppressed by debounce")
		return
	}

	isLocked, err := locker.IsSessionLocked()
	if err != nil {
		log.WithError(err).Warn("failed to check session lock state")
		return
	}
	if !isLocked {
		log.Debug("session is not locked — no prompt")
		return
	}

	*lastPromptAt = time.Now()
	log.Info("sending unlock prompt to telegram")
	go notifier.AskUnlock()
}
