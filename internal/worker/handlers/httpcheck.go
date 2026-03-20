package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// HTTPCheckHandler makes a GET request to a URL and returns the status code.
// Payload: {"url": "https://example.com"}
// Result:  {"url": "https://example.com", "status_code": 200}
type HTTPCheckHandler struct{}

type httpCheckPayload struct {
	URL string `json:"url"`
}

type httpCheckResult struct {
	URL        string `json:"url"`
	StatusCode int    `json:"status_code"`
}

// Type returns "httpcheck".
func (h *HTTPCheckHandler) Type() string { return "httpcheck" }

// Execute makes a GET request to the specified URL using the provided context.
func (h *HTTPCheckHandler) Execute(ctx context.Context, payload []byte) ([]byte, error) {
	var p httpCheckPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("parsing httpcheck payload: %w", err)
	}
	if p.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	resp.Body.Close()

	return json.Marshal(httpCheckResult{
		URL:        p.URL,
		StatusCode: resp.StatusCode,
	})
}
