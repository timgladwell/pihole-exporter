package piholetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func StartStubPiholeServer(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth":
			writeJSON(t, w, map[string]any{
				"session": map[string]any{
					"valid":    true,
					"totp":     false,
					"sid":      "sid-1",
					"csrf":     "csrf-1",
					"validity": 300,
				},
			})
		case "/api/stats/query_types":
			writeJSON(t, w, map[string]any{"types": map[string]any{"A": 1}})
		case "/api/stats/summary":
			writeJSON(t, w, map[string]any{
				"queries": map[string]any{
					"total":           1,
					"blocked":         0,
					"percent_blocked": 0,
					"unique_domains":  1,
					"forwarded":       1,
					"cached":          0,
					"frequency":       1,
					"types":           map[string]any{"A": 1},
					"status":          map[string]any{"FORWARDED": 1},
					"replies":         map[string]any{"IP": 1},
				},
				"clients": map[string]any{
					"active": 1,
					"total":  1,
				},
				"gravity": map[string]any{
					"domains_being_blocked": 1,
					"last_update":           1,
				},
			})
		case "/api/stats/top_clients", "/api/stats/top_domains":
			writeJSON(t, w, map[string]any{
				"total_queries":   1,
				"blocked_queries": 0,
			})
		case "/api/stats/upstreams":
			writeJSON(t, w, map[string]any{
				"forwarded_queries": 1,
				"total_queries":     1,
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))

	t.Cleanup(server.Close)
	return server
}

func writeJSON(t *testing.T, w http.ResponseWriter, value map[string]any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write response: %v", err)
	}
}
