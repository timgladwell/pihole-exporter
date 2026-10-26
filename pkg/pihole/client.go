package pihole

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func (c *AuthClient) GetJSONMap(ctx context.Context, path string) (map[string]any, error) {
	path = "/" + strings.TrimLeft(path, "/")

	resp, sid, err := c.get(ctx, path)
	if err != nil {
		return nil, err
	}
	// Pi-hole can drop a session before our clock expires it (restart,
	// eviction). Re-authenticate and retry once. Only once: a fresh session
	// that is also rejected means the credentials or API are wrong, and
	// retrying would only burn Pi-hole API seats.
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		c.Invalidate(sid)
		if resp, _, err = c.get(ctx, path); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var errResp apiErrorResponse
		if decodeErr := json.NewDecoder(resp.Body).Decode(&errResp); decodeErr == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("query pihole %s: %s: %s", path, resp.Status, errResp.Error.Message)
		}
		return nil, fmt.Errorf("query pihole %s: %s", path, resp.Status)
	}

	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()

	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode pihole %s response: %w", path, err)
	}

	return out, nil
}

// get sends one authenticated GET and returns the response with the SID it
// was sent under.
func (c *AuthClient) get(ctx context.Context, path string) (*http.Response, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.resolve("/api"+path), nil)
	if err != nil {
		return nil, "", fmt.Errorf("create pihole request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	if err := c.AddAuthHeaders(ctx, req); err != nil {
		return nil, "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("query pihole %s: %w", path, err)
	}
	return resp, req.Header.Get("X-FTL-SID"), nil
}
