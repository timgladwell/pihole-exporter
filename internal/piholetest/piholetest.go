// Package piholetest holds the Pi-hole test doubles: the validating mock
// (NewPihole) for tests inside the harness, and the plain stub (Handler) for
// tests against the built binary and container. Both serve the fixtures under
// testdata/, so they cannot disagree about what Pi-hole returns.
package piholetest

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FTLVersion is the Pi-hole FTL release the fixtures model. The API is served
// by FTL, so this, not the Pi-hole core version, is what an API change tracks.
const FTLVersion = "v6.7.1"

// Fixtures are hand-written to the FTL API shape until #39 captures them from a
// real Pi-hole. Values are distinct so a value reported under the wrong metric
// fails a test, and summary's queries.frequency and queries.replies.NODATA are
// deliberately 0.
//
//go:embed testdata
var testdata embed.FS

// fixtures maps an API path ("/api/stats/summary") to its response body.
var fixtures = loadFixtures()

func loadFixtures() map[string][]byte {
	root := "testdata/ftl-" + FTLVersion
	out := make(map[string][]byte)
	err := fs.WalkDir(testdata, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := testdata.ReadFile(path)
		if err != nil {
			return err
		}
		out["/api"+strings.TrimSuffix(strings.TrimPrefix(path, root), ".json")] = body
		return nil
	})
	if err != nil {
		panic(err)
	}
	return out
}

// authResponse is Pi-hole's reply to a successful POST /api/auth.
func authResponse(sid, csrf string) map[string]any {
	return map[string]any{
		"session": map[string]any{
			"valid":    true,
			"totp":     false,
			"sid":      sid,
			"csrf":     csrf,
			"validity": 1800,
			"message":  "app-password correct",
		},
		"took": 0.000412,
	}
}

func StartStubPiholeServer(t *testing.T) *httptest.Server {
	server := httptest.NewServer(Handler(t.Errorf))

	t.Cleanup(server.Close)
	return server
}

// Handler serves the subset of the Pi-hole API the exporter scrapes, and
// validates nothing: it is for the deployable tests, which confirm packaging.
// fail is called for paths the exporter should never request: t.Errorf under
// test, and log.Printf in the standalone stub the image system test runs
// against.
func Handler(fail func(format string, args ...any)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth" {
			// The exporter deletes its session on shutdown to release the
			// Pi-hole API seat.
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeJSON(fail, w, http.StatusOK, authResponse("sid-1", "csrf-1"))
			return
		}

		body, ok := fixtures[r.URL.Path]
		if !ok {
			fail("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

func writeJSON(fail func(format string, args ...any), w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		fail("write response: %v", err)
	}
}
