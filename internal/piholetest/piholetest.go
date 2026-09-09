package piholetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func StartStubPiholeServer(t *testing.T) *httptest.Server {
	server := httptest.NewServer(Handler(t.Fatalf))

	t.Cleanup(server.Close)
	return server
}

// Handler serves the subset of the Pi-hole API the exporter scrapes. fail is
// called for paths the exporter should never request: t.Fatalf under test, and
// log.Printf in the standalone stub the image system test runs against.
func Handler(fail func(format string, args ...any)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth":
			writeJSON(fail, w, map[string]any{
				"session": map[string]any{
					"valid":    true,
					"totp":     false,
					"sid":      "sid-1",
					"csrf":     "csrf-1",
					"validity": 300,
				},
			})
		case "/api/stats/query_types":
			writeJSON(fail, w, map[string]any{"types": map[string]any{"A": 1}})
		case "/api/stats/summary":
			writeJSON(fail, w, map[string]any{
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
			writeJSON(fail, w, map[string]any{
				"total_queries":   1,
				"blocked_queries": 0,
			})
		case "/api/stats/upstreams":
			writeJSON(fail, w, map[string]any{
				"forwarded_queries": 1,
				"total_queries":     1,
			})
		default:
			fail("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func writeJSON(fail func(format string, args ...any), w http.ResponseWriter, value map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		fail("write response: %v", err)
	}
}
