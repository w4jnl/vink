package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// sendJSON posts a JSON body and reads the answer the way every HTTP
// notifier does: any 2xx is success, anything else is an error with the
// first line of the response.
func sendJSON(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body any, kind string) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent(""))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s returned %s: %s", kind, resp.Status, strings.TrimSpace(string(snippet)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

// severity words an event for systems that rank alerts.
func severity(n Notification) string {
	switch n.Kind() {
	case "down":
		return "critical"
	case "late":
		return "warning"
	}
	return "info"
}

// colour is the state colour for cards: down red, late amber, up green.
func colour(n Notification) string {
	switch n.Kind() {
	case "down":
		return "#e05d44"
	case "late":
		return "#dfb317"
	case "up":
		return "#44cc11"
	}
	return "#9f9f9f"
}
