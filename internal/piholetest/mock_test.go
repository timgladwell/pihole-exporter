package piholetest

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// recordingT captures the mock's failures instead of failing the test, so a
// test can assert that the mock reports a bad request.
type recordingT struct {
	testing.TB
	mu     sync.Mutex
	errors []string
}

func (r *recordingT) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, format)
}

func (r *recordingT) failures() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errors)
}

func newRecordedMock(t *testing.T) (*Mock, *recordingT) {
	t.Helper()
	rt := &recordingT{TB: t}
	return New(rt), rt
}

func send(t *testing.T, m *Mock, method, path, body string, header map[string]string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(method, BaseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := m.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestMockRepliesToAMatchingRequest(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodGet, "/api/thing").Reply(http.StatusTeapot, `{"ok":true}`)

	resp := send(t, m, http.MethodGet, "/api/thing", "", nil)

	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
	if got, _ := io.ReadAll(resp.Body); string(got) != `{"ok":true}` {
		t.Fatalf("body = %s, want {\"ok\":true}", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	m.verify()
	if n := rt.failures(); n != 0 {
		t.Fatalf("mock reported %v, want no failures", rt.errors)
	}
}

func TestMockFailsAnUnexpectedRequest(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)

	resp := send(t, m, http.MethodGet, "/api/stats/summary", "", nil)

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
	if rt.failures() == 0 {
		t.Fatal("mock reported nothing for an unexpected request")
	}
}

func TestMockFailsAnExpectationThatNeverArrived(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodGet, "/api/stats/summary")

	m.verify()

	if rt.failures() == 0 {
		t.Fatal("mock reported nothing for an expected request that never arrived")
	}
}

func TestMockFailsARequestBeyondItsExpectedCount(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodGet, "/api/thing").Times(1)

	send(t, m, http.MethodGet, "/api/thing", "", nil)
	if rt.failures() != 0 {
		t.Fatalf("first request: mock reported %v, want no failures", rt.errors)
	}
	send(t, m, http.MethodGet, "/api/thing", "", nil)

	if rt.failures() == 0 {
		t.Fatal("mock reported nothing for a second request expected once")
	}
}

func TestMockFailsAHeaderWithTheWrongValue(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodGet, "/api/thing").Header("X-FTL-SID", "sid-1")

	send(t, m, http.MethodGet, "/api/thing", "", map[string]string{"X-FTL-SID": "sid-2"})

	if rt.failures() == 0 {
		t.Fatal("mock reported nothing for a wrong header value")
	}
}

func TestMockFailsAMissingHeader(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodGet, "/api/thing").Header("X-FTL-SID", "sid-1")

	send(t, m, http.MethodGet, "/api/thing", "", nil)

	if rt.failures() == 0 {
		t.Fatal("mock reported nothing for a missing header")
	}
}

func TestMockMatchesAJSONBodyInAnyKeyOrder(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodPost, "/api/thing").JSONBody(map[string]any{"a": 1, "b": "two"})

	send(t, m, http.MethodPost, "/api/thing", `{ "b": "two", "a": 1 }`, nil)

	if rt.failures() != 0 {
		t.Fatalf("mock reported %v, want no failures", rt.errors)
	}
}

func TestMockFailsAJSONBodyWithTheWrongType(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodPost, "/api/thing").JSONBody(map[string]any{"a": 1})

	send(t, m, http.MethodPost, "/api/thing", `{"a":"1"}`, nil)

	if rt.failures() == 0 {
		t.Fatal("mock reported nothing for a string where a number was expected")
	}
}

func TestMockFailsAJSONBodyWithAnExtraKey(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodPost, "/api/thing").JSONBody(map[string]any{"a": 1})

	send(t, m, http.MethodPost, "/api/thing", `{"a":1,"b":2}`, nil)

	if rt.failures() == 0 {
		t.Fatal("mock reported nothing for an unexpected JSON key")
	}
}

func TestMockFailsAJSONBodyThatIsNotJSON(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodPost, "/api/thing").JSONBody(map[string]any{"a": 1})

	send(t, m, http.MethodPost, "/api/thing", `a=1`, nil)

	if rt.failures() == 0 {
		t.Fatal("mock reported nothing for a form-encoded body where JSON was expected")
	}
}

func TestMockFailsATextBodyThatDiffersByOneByte(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodPost, "/api/thing").Body("hello")

	send(t, m, http.MethodPost, "/api/thing", "hello\n", nil)

	if rt.failures() == 0 {
		t.Fatal("mock reported nothing for a text body with a trailing newline")
	}
}

func TestMockFailsARequestArrivingBeforeItsPrerequisite(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	login := m.Expect(http.MethodPost, "/api/auth")
	m.Expect(http.MethodGet, "/api/stats/summary").After(login)

	send(t, m, http.MethodGet, "/api/stats/summary", "", nil)

	if rt.failures() == 0 {
		t.Fatal("mock reported nothing for a stats request before login")
	}
}

func TestMockMatchesUnorderedExpectationsInAnyOrder(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.Expect(http.MethodGet, "/api/a")
	m.Expect(http.MethodGet, "/api/b")

	send(t, m, http.MethodGet, "/api/b", "", nil)
	send(t, m, http.MethodGet, "/api/a", "", nil)
	m.verify()

	if rt.failures() != 0 {
		t.Fatalf("mock reported %v, want no failures", rt.errors)
	}
}

func TestExpectStatsRepliesWithTheFixture(t *testing.T) {
	t.Parallel()
	m, rt := newRecordedMock(t)
	m.ExpectStats("/api/stats/summary", "sid-1", "csrf-1")

	resp := send(t, m, http.MethodGet, "/api/stats/summary", "", map[string]string{
		"Accept": "application/json", "User-Agent": UserAgent, "X-FTL-SID": "sid-1", "X-FTL-CSRF": "csrf-1",
	})

	if got, _ := io.ReadAll(resp.Body); string(got) != string(fixtures["/api/stats/summary"]) {
		t.Fatalf("body = %s, want the summary fixture", got)
	}
	if rt.failures() != 0 {
		t.Fatalf("mock reported %v, want no failures", rt.errors)
	}
}
