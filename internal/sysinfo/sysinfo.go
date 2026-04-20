package sysinfo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	ipProvider = "https://api.ipify.org"
	httpLimit  = 128
)

// ExternalIP fetches the public IP of the host from a third-party service.
func ExternalIP(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ipProvider, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "dynamic-lock-agent")

	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return "", fmt.Errorf("external ip: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("external ip: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, httpLimit))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}
