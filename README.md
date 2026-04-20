# dynamic-lock

Bluetooth-based screen locker for Linux — the Windows Dynamic Lock idea,
done properly on top of `systemd-logind`.

When your phone drops out of Bluetooth range, your screen locks. When you
come back, a Telegram bot asks you to confirm the unlock — no automatic
unlock without your tap.

---

## Features

- **Lock on Bluetooth loss** with configurable debounce (N consecutive misses)
- **Unlock with confirmation** over Telegram — inline buttons, no auto-unlock
- **Desktop-agnostic** — talks to `logind` directly, works on any DE whose
  lockscreen honours `LockedHint` (tested on KDE Plasma; should work on
  GNOME, Sway, etc.)
- **Telegram bot** (optional) with a useful command set:
  `/status`, `/bt`, `/uptime`, `/ip`, `/lock`, `/unlock`, `/sleep`, `/logs`,
  `/ping`, `/whoami`, `/help`
- **Push notifications** on lock and on reconnect prompts
- **Safe by default** — whitelist on every command and callback, explicit
  `switch` dispatch, no shell execution from user input, MAC validation,
  fail-closed if the Telegram whitelist is missing

## Requirements

- Linux with `systemd` (uses `loginctl` and `systemctl`)
- `bluez` for `bluetoothctl`
- A lockscreen that integrates with `logind` (KDE, GNOME, etc.)
- Go 1.21+ to build from source
- Your phone paired and **trusted** in Bluetooth

## Install

### From source

```bash
git clone https://github.com/ylkhn/dynamic-lock.git
cd dynamic-lock
go build -o dynamic-lock-agent ./cmd/agent
```

### Arch / CachyOS prerequisites

```bash
sudo pacman -S go bluez bluez-utils
```

## Configuration

Two interchangeable formats. Environment variables always override file
values.

### YAML — `config.yaml`

```yaml
device_mac: "AA:BB:CC:DD:EE:FF"   # your phone's MAC
check_interval: "5s"               # Go duration string: 5s, 10s, 1m
fail_threshold: 3                  # consecutive misses before locking
log_level: "info"                  # debug | info | warning | error

telegram:                          # optional — leave bot_token empty to disable
  bot_token: ""
  allowed_user_id: 0
```

### .env

```bash
DEVICE_MAC=AA:BB:CC:DD:EE:FF
CHECK_INTERVAL=5s
FAIL_THRESHOLD=3
LOG_LEVEL=info

# Telegram (optional). If BOT_TOKEN is set, ALLOWED_USER_ID MUST be set —
# the agent refuses to start without a whitelist.
TELEGRAM_BOT_TOKEN=
TELEGRAM_ALLOWED_USER_ID=
```

### Finding your phone's MAC

```bash
bluetoothctl devices
```

### Telegram setup (optional)

1. Create a bot with [@BotFather](https://t.me/BotFather) → grab the token
2. Get your Telegram user ID from [@userinfobot](https://t.me/userinfobot)
3. Fill `TELEGRAM_BOT_TOKEN` and `TELEGRAM_ALLOWED_USER_ID`
4. Start a chat with your bot (press **Start**) so Telegram can deliver
   messages to you

## Run

### Manual

```bash
./dynamic-lock-agent --config config.yaml
# or
./dynamic-lock-agent --config .env
```

### As a systemd user service

```bash
# 1. Install the binary
mkdir -p ~/.local/bin
install -m 755 dynamic-lock-agent ~/.local/bin/

# 2. Install the config
mkdir -p ~/.config/dynamic-lock
cp config.yaml ~/.config/dynamic-lock/        # or .env

# 3. Install the unit
mkdir -p ~/.config/systemd/user
cp dynamic-lock.service ~/.config/systemd/user/

# 4. Enable & start
systemctl --user daemon-reload
systemctl --user enable --now dynamic-lock

# 5. Check
systemctl --user status dynamic-lock
journalctl --user -u dynamic-lock -f
```

Want the service to keep running after logout / across reboots without
you logging in first?

```bash
sudo loginctl enable-linger $USER
```

### Updating the binary while the service is running

Linux refuses to overwrite a running executable (`ETXTBSY`). Use `install`
— it writes to a temp file and renames atomically:

```bash
install -m 755 dynamic-lock-agent ~/.local/bin/
systemctl --user restart dynamic-lock
```

## Telegram command reference

### Control

| Command    | Action                                              |
|------------|-----------------------------------------------------|
| `/lock`    | Lock the screen (`loginctl lock-session`)           |
| `/unlock`  | Unlock the screen (`loginctl unlock-session`)       |
| `/sleep`   | Suspend the machine (`systemctl suspend`)           |

### Status

| Command    | Action                                              |
|------------|-----------------------------------------------------|
| `/status`  | Bluetooth state + uptime in one reply               |
| `/bt`      | Bluetooth state only                                |
| `/uptime`  | How long the agent has been running                 |
| `/ip`      | External IP (via `api.ipify.org`)                   |

### Service

| Command    | Action                                              |
|------------|-----------------------------------------------------|
| `/ping`    | Replies `ok` — liveness probe                       |
| `/whoami`  | Returns your Telegram user ID                       |
| `/logs`    | Last 20 log lines from the in-memory ring buffer    |
| `/help`    | Full command list                                   |

Anything else, and any message from a user ID that is not the whitelisted
one, is silently dropped and logged as `ignoring command from unauthorized
user`.

### Unlock prompt with confirmation

On a disconnected → connected Bluetooth edge, the agent queries
`IsSessionLocked()` from `logind`. If the session is actually locked, it
sends:

> 📶 Ты рядом. Разблокировать ПК?
> [ ✅ Unlock ] [ ❌ Cancel ]

- `✅ Unlock` → calls `loginctl unlock-session <sid>`
- `❌ Cancel` → does nothing

Key safety properties:

- No automatic unlock — always requires your tap
- Callback queries are checked against the same whitelist as commands
- 30-second debounce between prompts — protects against Bluetooth flapping
- Lock state is queried from `logind`, not from in-process memory, so
  manual unlocks are respected

## Architecture

```
cmd/agent/main.go          entry point, main loop, graceful shutdown
internal/bluetooth/        CheckConnected(mac) via bluetoothctl
internal/locker/           Lock / Unlock / Suspend / IsSessionLocked
internal/sysinfo/          ExternalIP
internal/telegram/         bot, callbacks, notifications
internal/config/           .env / YAML loader with env overrides
pkg/logger/                logrus + in-memory ring buffer (for /logs)
```

Two interfaces keep the main loop decoupled from Telegram:

```go
// What the bot needs from the agent
type Service interface {
    BluetoothStatus() (bool, error)
    Lock() error
    Unlock() error
    Suspend() error
    IsSessionLocked() (bool, error)
    Uptime() time.Duration
    ExternalIP(ctx context.Context) (string, error)
    LogTail(n int) []string
}

// What the main loop uses to push events out
type Notifier interface {
    Notify(text string)
    AskUnlock()
}
```

A future REST / WebApp layer can implement the same `Service` interface
and get the whole feature set for free.

## Security

- MAC address is validated against a strict regex before being passed to
  `bluetoothctl` (exec args, no shell)
- All subprocess calls use `exec.CommandContext` with a bounded timeout
- Telegram whitelist is enforced on **both** commands and inline-button
  callbacks; unknown callback data is rejected (no unlock)
- `telegram_bot_token` set without `telegram_allowed_user_id` causes the
  agent to refuse to start — fail-closed rather than open-to-world
- Lock/unlock/suspend decisions never depend on user-supplied strings;
  dispatch is an explicit `switch` over hard-coded constants

If you find a security issue, please open a private report rather than a
public issue.

## Roadmap

- REST / WebApp controller (reuses `Service`)
- Multiple trusted devices (`DEVICE_MACS=[]`)
- RSSI-based proximity lock instead of binary connected / not
- Optional auto-unlock window (N seconds after explicit `/unlock` consent)

## Troubleshooting

**Bot is silent.** Check `journalctl --user -u dynamic-lock -n 50`. Look
for `telegram bot started` (good) or `telegram bot disabled error=...`
(bad — usually an invalid token). Verify the token format: it must be
`<bot_id>:<hash>`, e.g. `7123456789:AAH...`.

**`/unlock` does nothing on KDE.** Test `loginctl unlock-session <sid>`
manually from a second terminal while the screen is locked. If that
doesn't work either, it's a polkit / kscreenlocker config issue, not the
agent — the required rule is usually already in `/etc/polkit-1/rules.d/`
on a stock KDE install.

**`Text file busy` while copying the binary.** The service is running.
Use `install -m 755 ...` (atomic) or `systemctl --user stop
dynamic-lock` first.

**`CHECK_INTERVAL` in .env rejected.** Use Go duration strings:
`5s`, `10s`, `1m30s`. Plain integers are not accepted.

## Development

```bash
go vet ./...
go build ./...
go test ./...   # no tests yet — PRs welcome
```

## License

TBD — pick the license you prefer (`MIT`, `Apache-2.0`, etc.) and drop
it in as `LICENSE` before publishing.

## Contributing

PRs and issues welcome. Useful starting points:

- Add `internal/api/` for REST control
- Add tests for `internal/config` and `internal/locker`
- Port the `/logs` ring buffer to a file-backed rotator for long-running
  deployments
