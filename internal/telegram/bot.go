package telegram

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/sirupsen/logrus"
)

// Service is the minimal interface the bot uses to interact with the agent.
// Kept intentionally small so a future WebApp / REST layer can reuse it
// without pulling in Telegram-specific concerns.
type Service interface {
	BluetoothStatus() (connected bool, err error)
	Lock() error
	Unlock() error
	Suspend() error
	IsSessionLocked() (bool, error)
	Uptime() time.Duration
	ExternalIP(ctx context.Context) (string, error)
	LogTail(n int) []string
}

// Callback data values. Kept short and opaque — pure constants, never built
// from user input, so there is no way for a caller to influence dispatch.
const (
	cbUnlock = "unlock"
	cbCancel = "cancel"
)

type Config struct {
	Token         string
	AllowedUserID int64
}

type Bot struct {
	api *tgbotapi.BotAPI
	cfg Config
	svc Service
	log *logrus.Logger
}

func New(cfg Config, svc Service, log *logrus.Logger) (*Bot, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("telegram: bot token is empty")
	}
	if cfg.AllowedUserID <= 0 {
		return nil, fmt.Errorf("telegram: allowed_user_id must be set (whitelist is required)")
	}
	api, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	return &Bot{api: api, cfg: cfg, svc: svc, log: log}, nil
}

// Run blocks until ctx is cancelled. Intended to be launched as a goroutine.
func (b *Bot) Run(ctx context.Context) {
	b.log.WithFields(logrus.Fields{
		"bot":          b.api.Self.UserName,
		"allowed_user": b.cfg.AllowedUserID,
	}).Info("telegram bot started")

	upd := tgbotapi.NewUpdate(0)
	upd.Timeout = 30
	updates := b.api.GetUpdatesChan(upd)

	go func() {
		<-ctx.Done()
		b.api.StopReceivingUpdates()
	}()

	for update := range updates {
		switch {
		case update.CallbackQuery != nil:
			b.handleCallback(update.CallbackQuery)
		case update.Message != nil && update.Message.IsCommand():
			b.handleCommand(update.Message)
		}
	}

	b.log.Info("telegram bot stopped")
}

// ---- notifications ----------------------------------------------------------

// Notify sends a plain text message to the whitelisted user. Safe to call on
// a nil receiver.
func (b *Bot) Notify(text string) {
	if b == nil {
		return
	}
	msg := tgbotapi.NewMessage(b.cfg.AllowedUserID, text)
	if _, err := b.api.Send(msg); err != nil {
		b.log.WithError(err).Warn("telegram notify failed")
	}
}

// AskUnlock sends a prompt with inline Unlock/Cancel buttons to the
// whitelisted user. No action is taken on the system until the user taps
// a button and the callback handler runs.
func (b *Bot) AskUnlock() {
	if b == nil {
		return
	}
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Unlock", cbUnlock),
			tgbotapi.NewInlineKeyboardButtonData("❌ Cancel", cbCancel),
		),
	)
	msg := tgbotapi.NewMessage(b.cfg.AllowedUserID, "📶 Ты рядом. Разблокировать ПК?")
	msg.ReplyMarkup = keyboard
	if _, err := b.api.Send(msg); err != nil {
		b.log.WithError(err).Warn("telegram ask-unlock failed")
		return
	}
	b.log.Info("sent unlock prompt")
}

// ---- callback handling ------------------------------------------------------

func (b *Bot) handleCallback(q *tgbotapi.CallbackQuery) {
	var userID int64
	var username string
	if q.From != nil {
		userID = q.From.ID
		username = q.From.UserName
	}
	if userID != b.cfg.AllowedUserID {
		b.log.WithFields(logrus.Fields{
			"user_id":  userID,
			"username": username,
			"data":     q.Data,
		}).Warn("ignoring callback from unauthorized user")
		return
	}

	b.log.WithFields(logrus.Fields{
		"user_id": userID,
		"data":    q.Data,
	}).Info("handling telegram callback")

	switch q.Data {
	case cbUnlock:
		if err := b.svc.Unlock(); err != nil {
			b.log.WithError(err).Error("unlock failed")
			b.ackCallback(q, "unlock failed")
			b.replaceMessage(q, fmt.Sprintf("❌ unlock failed: %v", err))
			return
		}
		b.log.Info("unlock confirmed via telegram")
		b.ackCallback(q, "🔓 unlocked")
		b.replaceMessage(q, "🔓 unlocked")

	case cbCancel:
		b.log.Info("unlock cancelled via telegram")
		b.ackCallback(q, "cancelled")
		b.replaceMessage(q, "❌ cancelled")

	default:
		// Unknown action — do nothing, don't unlock.
		b.log.WithField("data", q.Data).Warn("ignoring unknown callback data")
		b.ackCallback(q, "unknown action")
	}
}

func (b *Bot) ackCallback(q *tgbotapi.CallbackQuery, text string) {
	cb := tgbotapi.NewCallback(q.ID, text)
	if _, err := b.api.Request(cb); err != nil {
		b.log.WithError(err).Warn("telegram callback ack failed")
	}
}

func (b *Bot) replaceMessage(q *tgbotapi.CallbackQuery, text string) {
	if q.Message == nil {
		return
	}
	edit := tgbotapi.NewEditMessageText(q.Message.Chat.ID, q.Message.MessageID, text)
	if _, err := b.api.Send(edit); err != nil {
		b.log.WithError(err).Warn("telegram edit failed")
	}
}

// ---- command handling -------------------------------------------------------

func (b *Bot) handleCommand(m *tgbotapi.Message) {
	var userID int64
	var username string
	if m.From != nil {
		userID = m.From.ID
		username = m.From.UserName
	}

	if userID != b.cfg.AllowedUserID {
		b.log.WithFields(logrus.Fields{
			"user_id":  userID,
			"username": username,
			"command":  m.Command(),
		}).Warn("ignoring command from unauthorized user")
		return
	}

	cmd := m.Command()
	b.log.WithFields(logrus.Fields{
		"user_id": userID,
		"command": cmd,
	}).Info("handling telegram command")

	// Explicit dispatch — no dynamic lookup, no shell execution.
	switch cmd {
	case "ping":
		b.reply(m, "ok")

	case "whoami":
		b.reply(m, fmt.Sprintf("user_id: %d", userID))

	case "bt":
		b.reply(m, b.btLine())

	case "uptime":
		b.reply(m, formatDuration(b.svc.Uptime()))

	case "status":
		b.reply(m, fmt.Sprintf(
			"bluetooth: %s\nuptime:    %s",
			b.btLine(),
			formatDuration(b.svc.Uptime()),
		))

	case "ip":
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		ip, err := b.svc.ExternalIP(ctx)
		if err != nil {
			b.reply(m, fmt.Sprintf("error: %v", err))
			return
		}
		b.reply(m, ip)

	case "lock":
		if err := b.svc.Lock(); err != nil {
			b.reply(m, fmt.Sprintf("lock failed: %v", err))
			return
		}
		b.reply(m, "🔒 locked")

	case "unlock":
		if err := b.svc.Unlock(); err != nil {
			b.reply(m, fmt.Sprintf("unlock failed: %v", err))
			return
		}
		b.reply(m, "🔓 unlocked")

	case "sleep":
		// ACK first — the system may go offline mid-suspend.
		b.reply(m, "💤 suspending...")
		if err := b.svc.Suspend(); err != nil {
			b.reply(m, fmt.Sprintf("suspend failed: %v", err))
		}

	case "logs":
		lines := b.svc.LogTail(20)
		if len(lines) == 0 {
			b.reply(m, "no logs yet")
			return
		}
		b.replyLogs(m, lines)

	case "help", "start":
		b.reply(m, helpText)

	default:
		b.reply(m, "unknown command\n\n"+helpText)
	}
}

const helpText = `commands:
/status  — bluetooth + uptime
/bt      — bluetooth state
/uptime  — agent uptime
/ip      — external IP
/lock    — lock screen
/unlock  — unlock screen
/sleep   — suspend
/logs    — last 20 log lines
/ping    — liveness probe
/whoami  — your user_id`

func (b *Bot) btLine() string {
	connected, err := b.svc.BluetoothStatus()
	switch {
	case err != nil:
		return fmt.Sprintf("error: %v", err)
	case connected:
		return "📶 connected"
	default:
		return "🔻 disconnected"
	}
}

// ---- reply helpers ----------------------------------------------------------

func (b *Bot) reply(m *tgbotapi.Message, text string) {
	msg := tgbotapi.NewMessage(m.Chat.ID, text)
	msg.ReplyToMessageID = m.MessageID
	if _, err := b.api.Send(msg); err != nil {
		b.log.WithError(err).Warn("telegram send failed")
	}
}

func (b *Bot) replyLogs(m *tgbotapi.Message, lines []string) {
	const telegramLimit = 3800 // leave headroom under the 4096 cap
	body := strings.Join(lines, "\n")
	if len(body) > telegramLimit {
		body = "…\n" + body[len(body)-telegramLimit:]
	}
	text := "<pre>" + html.EscapeString(body) + "</pre>"
	msg := tgbotapi.NewMessage(m.Chat.ID, text)
	msg.ReplyToMessageID = m.MessageID
	msg.ParseMode = tgbotapi.ModeHTML
	if _, err := b.api.Send(msg); err != nil {
		b.log.WithError(err).Warn("telegram send failed")
	}
}

func formatDuration(d time.Duration) string {
	return d.Round(time.Second).String()
}
