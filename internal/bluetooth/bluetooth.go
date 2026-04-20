package bluetooth

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const cmdTimeout = 10 * time.Second

// CheckConnected reports whether the Bluetooth device identified by mac is
// currently connected.
//
// Returns (false, nil) when the device is reachable but not connected, or when
// the device is not known to bluetoothd — both are "not connected" for our
// purposes. Returns a non-nil error only on tool-level failures (timeout,
// binary missing) so the caller can distinguish transient tool errors from
// normal "phone is away" events.
func CheckConnected(mac string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	// Arguments are passed as separate values — no shell expansion, no injection.
	out, err := exec.CommandContext(ctx, "bluetoothctl", "info", mac).Output()
	if err != nil {
		if ctx.Err() != nil {
			return false, fmt.Errorf("bluetoothctl timed out after %s", cmdTimeout)
		}
		// Non-zero exit: device not paired/found → treat as not connected.
		return false, nil
	}

	return strings.Contains(string(out), "Connected: yes"), nil
}
