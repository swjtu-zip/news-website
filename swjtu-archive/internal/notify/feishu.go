// Package notify pushes sync-run alerts to a Feishu group webhook.
// It is entirely opt-in: with FEISHU_WEBHOOK_URL unset every call is a no-op.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Send posts text as a Feishu text message. It returns nil immediately when
// FEISHU_WEBHOOK_URL is empty, so callers never need to check configuration.
func Send(text string) error {
	webhook := strings.TrimSpace(os.Getenv("FEISHU_WEBHOOK_URL"))
	if webhook == "" {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu webhook: status %d", resp.StatusCode)
	}
	return nil
}

// severeMarkers are substrings that turn an ordinary per-feed failure into a
// run-aborting credential/access problem worth paging a human about.
var severeMarkers = []string{"403", "AccessDenied", "Access Denied", "密钥", "unauthorized"}

// IsSevere reports whether err looks like an access/credential failure that
// should abort the whole sync run rather than be recorded as one more
// per-feed failure.
func IsSevere(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, marker := range severeMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
