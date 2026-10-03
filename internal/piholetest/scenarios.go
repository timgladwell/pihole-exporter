package piholetest

import (
	"net/http"
	"sort"
	"strings"
)

// The helpers below spell out the requests an AuthClient sends, so a test
// names a scenario rather than listing headers. They hold no logic: each is
// an Expect with fixed headers, body and reply, which a test can still adjust.

// ExpectLogin expects POST /api/auth carrying password. It replies with
// Session("sid-1", "csrf-1") unless the test sets another Reply.
func (m *Mock) ExpectLogin(password string) *Expectation {
	return m.Expect(http.MethodPost, "/api/auth").
		Header("Content-Type", "application/json").
		Header("Accept", "application/json").
		Header("User-Agent", UserAgent).
		JSONBody(map[string]any{"password": password}).
		Reply(http.StatusOK, Session("sid-1", "csrf-1"))
}

// ExpectStats expects GET of an API path ("/api/stats/summary") under the
// session sid and csrf, and replies with that path's fixture.
func (m *Mock) ExpectStats(path, sid, csrf string) *Expectation {
	body, ok := fixtures[path]
	if !ok {
		panic("piholetest: no fixture for " + path)
	}
	return m.Expect(http.MethodGet, path).
		Header("Accept", "application/json").
		Header("User-Agent", UserAgent).
		Header("X-FTL-SID", sid).
		Header("X-FTL-CSRF", csrf).
		Reply(http.StatusOK, body)
}

// ExpectScrape expects one GET of every stats endpoint per scrape, under the
// session sid and csrf, none of them before login.
func (m *Mock) ExpectScrape(login *Expectation, sid, csrf string, scrapes int) {
	for _, path := range StatsPaths() {
		m.ExpectStats(path, sid, csrf).After(login).Times(scrapes)
	}
}

// ExpectLogout expects DELETE /api/auth releasing the session sid.
func (m *Mock) ExpectLogout(sid, csrf string) *Expectation {
	return m.Expect(http.MethodDelete, "/api/auth").
		Header("User-Agent", UserAgent).
		Header("X-FTL-SID", sid).
		Header("X-FTL-CSRF", csrf).
		Reply(http.StatusNoContent, nil)
}

// Session is Pi-hole's reply to a successful login.
func Session(sid, csrf string) map[string]any {
	return authResponse(sid, csrf)
}

// Unauthorized is Pi-hole's reply to a wrong password or a dead session.
func Unauthorized() map[string]any {
	return map[string]any{
		"error": map[string]any{"key": "unauthorized", "message": "Unauthorized", "hint": nil},
		"took":  0.000105,
	}
}

// StatsPaths lists the API paths with a fixture, sorted.
func StatsPaths() []string {
	var paths []string
	for path := range fixtures {
		if strings.HasPrefix(path, "/api/stats/") {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}
