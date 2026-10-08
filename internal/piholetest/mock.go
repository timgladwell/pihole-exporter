package piholetest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jarcoal/httpmock"
)

// BaseURL is where the mock answers. Nothing listens there: httpmock replaces
// the HTTP client's transport, so a request is handed to the mock in-process
// and never reaches the network.
const BaseURL = "http://pihole.test"

// UserAgent is what an AuthClient sends when no WithUserAgent option is given.
const UserAgent = "pihole-exporter"

// Mock is a strict, scenario-configured mock of the Pi-hole API. A test lists
// the requests it expects with Expect and the canned reply for each. A request
// that matches no remaining expectation fails the test, and so does an
// expectation still unmet when the test ends.
//
// Expectations match in any order unless one is declared After another.
type Mock struct {
	t         testing.TB
	transport *httpmock.MockTransport

	mu           sync.Mutex
	expectations []*Expectation
}

// Expectation is one request the test expects, and the reply to give it.
type Expectation struct {
	method string
	path   string
	header map[string]string

	body     []byte // compared byte for byte, unless isJSON
	isJSON   bool   // compare body as decoded JSON key/value pairs
	hasBody  bool
	times    int
	used     int
	after    *Expectation
	status   int
	reply    []byte
	replyCTy string
}

// New returns a mock with no expectations. Point an AuthClient at it with
// BaseURL and pihole.WithHTTPClient(mock.Client()).
func New(t testing.TB) *Mock {
	m := &Mock{t: t, transport: httpmock.NewMockTransport()}
	m.transport.RegisterNoResponder(func(r *http.Request) (*http.Response, error) {
		m.t.Errorf("Pi-hole mock: unexpected request %s %s", r.Method, r.URL.Path)
		return mismatchResponse(), nil
	})
	t.Cleanup(m.verify)
	return m
}

// Client returns an HTTP client whose requests the mock answers.
func (m *Mock) Client() *http.Client {
	return &http.Client{Transport: m.transport}
}

// Expect adds an expectation for method and path ("/api/auth"), met once and
// answered 200 with an empty body unless configured otherwise.
func (m *Mock) Expect(method, path string) *Expectation {
	e := &Expectation{method: method, path: path, header: map[string]string{}, times: 1, status: http.StatusOK}

	m.mu.Lock()
	m.expectations = append(m.expectations, e)
	m.mu.Unlock()

	m.transport.RegisterResponder(method, BaseURL+path, m.respond)
	return e
}

// Header requires the request to carry this header with exactly this value.
func (e *Expectation) Header(key, value string) *Expectation {
	e.header[key] = value
	return e
}

// Body requires the request body to match text byte for byte.
func (e *Expectation) Body(text string) *Expectation {
	e.body, e.isJSON, e.hasBody = []byte(text), false, true
	return e
}

// JSONBody requires the request body to be JSON with exactly the key/value
// pairs of value, in any key order. Types count: "1" does not match 1.
func (e *Expectation) JSONBody(value any) *Expectation {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err) // a test passed a value that cannot be JSON
	}
	e.body, e.isJSON, e.hasBody = body, true, true
	return e
}

// Times sets how many requests this expectation must meet.
func (e *Expectation) Times(n int) *Expectation {
	e.times = n
	return e
}

// After requires every request prev expects to have arrived before this one
// can match. Use it where order matters, such as logging in before anything
// else.
func (e *Expectation) After(prev *Expectation) *Expectation {
	e.after = prev
	return e
}

// Reply sets the response. The Pi-hole API speaks only JSON, so any body is
// labelled application/json: a string or []byte is sent as is (a fixture, or
// deliberately malformed JSON), and anything else is encoded. A nil body sends
// no body and no Content-Type.
func (e *Expectation) Reply(status int, body any) *Expectation {
	e.status = status
	switch b := body.(type) {
	case nil:
		e.reply, e.replyCTy = nil, ""
	case string:
		e.reply, e.replyCTy = []byte(b), "application/json"
	case []byte:
		e.reply, e.replyCTy = b, "application/json"
	default:
		encoded, err := json.Marshal(b)
		if err != nil {
			panic(err)
		}
		e.reply, e.replyCTy = encoded, "application/json"
	}
	return e
}

func (e *Expectation) String() string {
	return e.method + " " + e.path
}

func (m *Mock) respond(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var mismatches []string
	for _, e := range m.expectations {
		if e.method != r.Method || e.path != r.URL.Path || e.used >= e.times {
			continue
		}
		if e.after != nil && e.after.used < e.after.times {
			mismatches = append(mismatches, fmt.Sprintf("arrived before %s", e.after))
			continue
		}
		if diff := e.mismatch(r.Header, body); diff != "" {
			mismatches = append(mismatches, diff)
			continue
		}

		e.used++
		resp := httpmock.NewBytesResponse(e.status, e.reply)
		if e.replyCTy != "" {
			resp.Header.Set("Content-Type", e.replyCTy)
		}
		return resp, nil
	}

	if len(mismatches) == 0 {
		m.t.Errorf("Pi-hole mock: unexpected request %s %s (no expectation left for it)", r.Method, r.URL.Path)
	} else {
		m.t.Errorf("Pi-hole mock: %s %s matched no expectation:\n%s", r.Method, r.URL.Path, strings.Join(mismatches, "\n"))
	}
	return mismatchResponse(), nil
}

// mismatch describes how a request differs from e, or returns "" if it matches.
func (e *Expectation) mismatch(header http.Header, body []byte) string {
	var problems []string
	for key, want := range e.header {
		if got := header.Get(key); got != want {
			problems = append(problems, fmt.Sprintf("header %s = %q, want %q", key, got, want))
		}
	}

	if e.hasBody {
		if e.isJSON {
			var got, want any
			if err := json.Unmarshal(body, &got); err != nil {
				problems = append(problems, fmt.Sprintf("body is not JSON (%v): %q", err, body))
			} else {
				_ = json.Unmarshal(e.body, &want)
				if diff := cmp.Diff(want, got); diff != "" {
					problems = append(problems, "JSON body mismatch (-want +got):\n"+diff)
				}
			}
		} else if !bytes.Equal(body, e.body) {
			problems = append(problems, fmt.Sprintf("body = %q, want %q", body, e.body))
		}
	}

	return strings.Join(problems, "; ")
}

// verify fails the test for every expectation not fully met. New registers it
// with t.Cleanup.
func (m *Mock) verify() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range m.expectations {
		if e.used < e.times {
			m.t.Errorf("Pi-hole mock: expected %s %d time(s), got %d", e, e.times, e.used)
		}
	}
}

// mismatchResponse answers a request the test did not expect. The test has
// already failed; a 500 also makes the exporter treat it as an error rather
// than data.
func mismatchResponse() *http.Response {
	return httpmock.NewStringResponse(http.StatusInternalServerError, "Pi-hole mock: no matching expectation")
}
