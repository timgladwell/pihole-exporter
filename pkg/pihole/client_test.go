package pihole

import (
	"context"
	"net/http"
	"testing"

	"github.com/timgladwell/pihole-exporter/internal/piholetest"
)

const summaryPath = "/api/stats/summary"

// TestInvalidateIgnoresAStaleSession stands in for the concurrent case: a late
// request that failed on sid-1 must not discard the sid-2 session made since.
func TestInvalidateIgnoresAStaleSession(t *testing.T) {
	t.Parallel()
	client, mock, _ := newMockedClient(t)
	login1 := mock.ExpectLogin("app-password")
	mock.ExpectLogin("app-password").After(login1).Reply(http.StatusOK, piholetest.Session("sid-2", "csrf-2"))

	mustSession(t, client)
	client.Invalidate("sid-1")
	if got := mustSession(t, client).SID; got != "sid-2" {
		t.Fatalf("SID after Invalidate = %q, want sid-2", got)
	}
	client.Invalidate("sid-1")
	if got := mustSession(t, client).SID; got != "sid-2" {
		t.Fatalf("SID after stale Invalidate = %q, want sid-2", got)
	}
}

// expectRejectedThenRelogin declares login1, a 401 for sid-1 on path, and a
// second login yielding sid-2. It returns the second login.
func expectRejectedThenRelogin(mock *piholetest.Mock, path string) *piholetest.Expectation {
	login1 := mock.ExpectLogin("app-password")
	rejected := mock.ExpectStats(path, "sid-1", "csrf-1").After(login1).
		Reply(http.StatusUnauthorized, piholetest.Unauthorized())
	return mock.ExpectLogin("app-password").After(rejected).
		Reply(http.StatusOK, piholetest.Session("sid-2", "csrf-2"))
}

func TestGetJSONMapReauthenticatesWhenSessionIsRejected(t *testing.T) {
	t.Parallel()
	client, mock, _ := newMockedClient(t)
	login2 := expectRejectedThenRelogin(mock, summaryPath)
	mock.ExpectStats(summaryPath, "sid-2", "csrf-2").After(login2)

	if _, err := client.GetJSONMap(context.Background(), "/stats/summary"); err != nil {
		t.Fatalf("GetJSONMap() error = %v", err)
	}
}

func TestGetJSONMapFailsWhenAFreshSessionIsAlsoRejected(t *testing.T) {
	t.Parallel()
	client, mock, _ := newMockedClient(t)
	login2 := expectRejectedThenRelogin(mock, summaryPath)
	mock.ExpectStats(summaryPath, "sid-2", "csrf-2").After(login2).
		Reply(http.StatusUnauthorized, piholetest.Unauthorized())

	if _, err := client.GetJSONMap(context.Background(), "/stats/summary"); err == nil {
		t.Fatal("GetJSONMap() error = nil, want error")
	}
}

func TestGetJSONMapKeepsTheRecoveredSession(t *testing.T) {
	t.Parallel()
	client, mock, _ := newMockedClient(t)
	login1 := mock.ExpectLogin("app-password")
	first := mock.ExpectStats(summaryPath, "sid-1", "csrf-1").After(login1)
	rejected := mock.ExpectStats(summaryPath, "sid-1", "csrf-1").After(first).
		Reply(http.StatusUnauthorized, piholetest.Unauthorized())
	login2 := mock.ExpectLogin("app-password").After(rejected).
		Reply(http.StatusOK, piholetest.Session("sid-2", "csrf-2"))
	retried := mock.ExpectStats(summaryPath, "sid-2", "csrf-2").After(login2)
	mock.ExpectStats(summaryPath, "sid-2", "csrf-2").After(retried)

	for i := 1; i <= 3; i++ {
		if _, err := client.GetJSONMap(context.Background(), "/stats/summary"); err != nil {
			t.Fatalf("GetJSONMap() call %d error = %v", i, err)
		}
	}
}

func TestGetJSONMapDoesNotRetryOtherFailures(t *testing.T) {
	t.Parallel()
	client, mock, _ := newMockedClient(t)
	login := mock.ExpectLogin("app-password")
	mock.ExpectStats(summaryPath, "sid-1", "csrf-1").After(login).Reply(http.StatusInternalServerError, nil)

	if _, err := client.GetJSONMap(context.Background(), "/stats/summary"); err == nil {
		t.Fatal("GetJSONMap() error = nil, want error")
	}
}
