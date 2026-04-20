package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

var macRe = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

// Config holds all runtime parameters.
type Config struct {
	DeviceMAC     string
	CheckInterval time.Duration
	FailThreshold int
	LogLevel      string

	// Telegram (optional). If TelegramBotToken is empty, the bot is disabled.
	TelegramBotToken      string
	TelegramAllowedUserID int64
}

func defaults() *Config {
	return &Config{
		CheckInterval: 5 * time.Second,
		FailThreshold: 3,
		LogLevel:      "info",
	}
}

// Load reads config from a .env or .yaml file, then applies env var overrides.
// Env vars always take precedence.
func Load(path string) (*Config, error) {
	cfg := defaults()

	switch filepath.Ext(path) {
	case ".yaml", ".yml":
		if err := fromYAML(path, cfg); err != nil {
			return nil, err
		}
	default:
		if err := godotenv.Load(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("loading .env file: %w", err)
		}
	}

	if err := fromEnv(cfg); err != nil {
		return nil, err
	}

	return cfg, validate(cfg)
}

// ---- internal ---------------------------------------------------------------

type rawYAML struct {
	DeviceMAC     string `yaml:"device_mac"`
	CheckInterval string `yaml:"check_interval"`
	FailThreshold int    `yaml:"fail_threshold"`
	LogLevel      string `yaml:"log_level"`
	Telegram      struct {
		BotToken      string `yaml:"bot_token"`
		AllowedUserID int64  `yaml:"allowed_user_id"`
	} `yaml:"telegram"`
}

func fromYAML(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	var raw rawYAML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing yaml: %w", err)
	}
	if raw.DeviceMAC != "" {
		cfg.DeviceMAC = raw.DeviceMAC
	}
	if raw.CheckInterval != "" {
		d, err := time.ParseDuration(raw.CheckInterval)
		if err != nil {
			return fmt.Errorf("invalid check_interval %q: %w", raw.CheckInterval, err)
		}
		cfg.CheckInterval = d
	}
	if raw.FailThreshold > 0 {
		cfg.FailThreshold = raw.FailThreshold
	}
	if raw.LogLevel != "" {
		cfg.LogLevel = raw.LogLevel
	}
	if raw.Telegram.BotToken != "" {
		cfg.TelegramBotToken = raw.Telegram.BotToken
	}
	if raw.Telegram.AllowedUserID != 0 {
		cfg.TelegramAllowedUserID = raw.Telegram.AllowedUserID
	}
	return nil
}

func fromEnv(cfg *Config) error {
	if v := os.Getenv("DEVICE_MAC"); v != "" {
		cfg.DeviceMAC = v
	}
	if v := os.Getenv("CHECK_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid CHECK_INTERVAL %q: %w", v, err)
		}
		cfg.CheckInterval = d
	}
	if v := os.Getenv("FAIL_THRESHOLD"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid FAIL_THRESHOLD %q: %w", v, err)
		}
		cfg.FailThreshold = n
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.TelegramBotToken = v
	}
	if v := os.Getenv("TELEGRAM_ALLOWED_USER_ID"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid TELEGRAM_ALLOWED_USER_ID %q: %w", v, err)
		}
		cfg.TelegramAllowedUserID = n
	}
	return nil
}

func validate(cfg *Config) error {
	if cfg.DeviceMAC == "" {
		return fmt.Errorf("device MAC is required (DEVICE_MAC or device_mac)")
	}
	if !macRe.MatchString(cfg.DeviceMAC) {
		return fmt.Errorf("invalid MAC address %q (expected XX:XX:XX:XX:XX:XX)", cfg.DeviceMAC)
	}
	if cfg.CheckInterval <= 0 {
		return fmt.Errorf("check_interval must be positive")
	}
	if cfg.FailThreshold <= 0 {
		return fmt.Errorf("fail_threshold must be positive")
	}
	// Whitelist is mandatory if the bot is enabled — fail closed, not open.
	if cfg.TelegramBotToken != "" && cfg.TelegramAllowedUserID <= 0 {
		return fmt.Errorf("telegram_bot_token is set but telegram_allowed_user_id is empty — refusing to start without whitelist")
	}
	return nil
}
