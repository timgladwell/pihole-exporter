package pihole

import (
	"net/http"
	"testing"

	"github.com/timgladwell/pihole-exporter/internal/piholetest"
)

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
