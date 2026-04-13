package navidrome

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type ScanStatus struct {
	Scanning bool   `json:"scanning"`
	Count    int64  `json:"count"`
	Error    string `json:"error,omitempty"`
}

// StartScan triggers a Navidrome library scan and waits for it to complete.
func (c *Client) StartScan(ctx context.Context) error {
	params := c.subsonicParams()
	url := fmt.Sprintf("%s/rest/startScan?%s", c.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("startScan request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("startScan returned status %d", resp.StatusCode)
	}

	slog.Info("scan triggered, waiting for completion")
	return c.waitScanComplete(ctx, 10*time.Minute)
}

func (c *Client) waitScanComplete(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := c.getScanStatus(ctx)
		if err != nil {
			return err
		}
		if !status.Scanning {
			if status.Error != "" {
				return fmt.Errorf("scan completed with error: %s", status.Error)
			}
			slog.Info("scan complete", "count", status.Count)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("scan did not complete within %s", timeout)
}

func (c *Client) getScanStatus(ctx context.Context) (*ScanStatus, error) {
	params := c.subsonicParams()
	url := fmt.Sprintf("%s/rest/getScanStatus?%s", c.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var envelope struct {
		SubsonicResponse struct {
			ScanStatus ScanStatus `json:"scanStatus"`
		} `json:"subsonic-response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("failed to decode scan status: %w", err)
	}

	return &envelope.SubsonicResponse.ScanStatus, nil
}
