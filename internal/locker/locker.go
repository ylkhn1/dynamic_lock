package locker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const cmdTimeout = 10 * time.Second

// Lock locks the user's graphical session via loginctl.
func Lock() error {
	session, err := displaySession()
	if err != nil {
		return err
	}
	return runQuiet("loginctl", "lock-session", session)
}

// Unlock unlocks the user's graphical session via loginctl. Safe to call on
// an already-unlocked session — loginctl is a no-op in that case.
func Unlock() error {
	session, err := displaySession()
	if err != nil {
		return err
	}
	return runQuiet("loginctl", "unlock-session", session)
}

// Suspend puts the machine to sleep via systemctl. Polkit must allow the
// active user to suspend (the default on most desktop installs).
func Suspend() error {
	return runQuiet("systemctl", "suspend")
}

// IsSessionLocked reports whether the user's graphical session has the
// LockedHint set by logind. Queries loginctl directly — no reliance on
// in-process state that could be stale.
func IsSessionLocked() (bool, error) {
	session, err := displaySession()
	if err != nil {
		return false, err
	}
	if session == "" {
		return false, nil
	}
	hint, err := readLoginctl("show-session", session, "--value", "-p", "LockedHint")
	if err != nil {
		return false, fmt.Errorf("read LockedHint: %w", err)
	}
	return strings.TrimSpace(hint) == "yes", nil
}

// ---- internal ---------------------------------------------------------------

// displaySession returns the ID of the user's graphical session as reported
// by logind. From a user systemd service the caller has no session of its
// own, so we must resolve it explicitly instead of letting loginctl default
// to "self".
func displaySession() (string, error) {
	uid := strconv.Itoa(os.Getuid())
	out, err := readLoginctl("show-user", uid, "--value", "-p", "Display")
	if err != nil {
		return "", fmt.Errorf("resolve display session: %w", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", fmt.Errorf("no graphical session for uid %s", uid)
	}
	return id, nil
}

func runQuiet(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, name, args...).Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func readLoginctl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "loginctl", args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
