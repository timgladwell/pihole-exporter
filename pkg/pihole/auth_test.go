package pihole

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/timgladwell/pihole-exporter/internal/piholetest"
)

// newMockedClient returns an AuthClient wired to a fresh validating mock. The
// returned clock pointer drives the client's injected clock.
func newMockedClient(t *testing.T, opts ...AuthClientOption) (*AuthClient, *piholetest.Mock, *time.Time) {
	t.Helper()

	mock := piholetest.New(t)
	now := time.Unix(1000, 0)
	opts = append([]AuthClientOption{WithHTTPClient(mock.Client())}, opts...)
	client, err := NewAuthClient(piholetest.BaseURL, "app-password", opts...)
	if err != nil {
		t.Fatalf("NewAuthClient() error = %v", err)
	}
	client.now = func() time.Time { return now }
	return client, mock, &now
}

func mustSession(t *testing.T, client *AuthClient) Session {
	t.Helper()

	session, err := client.Session(context.Background())
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	return session
}

func TestSessionAuthenticatesAndCachesToken(t *testing.T) {
	t.Parallel()

	client, mock, _ := newMockedClient(t)
	mock.ExpectLogin("app-password")

	first := mustSession(t, client)
	second := mustSession(t, client)

	if first.SID != "sid-1" || second.SID != "sid-1" {
		t.Fatalf("unexpected SID values: first=%q second=%q", first.SID, second.SID)
	}
}

func TestSessionRefreshesWhenTokenIsNearExpiry(t *testing.T) {
	t.Parallel()

	client, mock, now := newMockedClient(t, WithRefreshSkew(30*time.Second))
	login1 := mock.ExpectLogin("app-password")
	logout := mock.ExpectLogout("sid-1", "csrf-1").After(login1)
	mock.ExpectLogin("app-password").After(logout).Reply(http.StatusOK, piholetest.Session("sid-2", "csrf-2"))

	first := mustSession(t, client)
	*now = now.Add(1771 * time.Second)
	second := mustSession(t, client)

	if first.SID != "sid-1" || second.SID != "sid-2" {
		t.Fatalf("SIDs = %q then %q, want sid-1 then sid-2", first.SID, second.SID)
	}
}

func TestLogoutDeletesSession(t *testing.T) {
	t.Parallel()

	client, mock, _ := newMockedClient(t)
	login := mock.ExpectLogin("app-password")
	mock.ExpectLogout("sid-1", "csrf-1").After(login)

	mustSession(t, client)
	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
}

func TestSessionAuthenticatesAgainAfterLogout(t *testing.T) {
	t.Parallel()

	client, mock, _ := newMockedClient(t)
	login1 := mock.ExpectLogin("app-password")
	logout := mock.ExpectLogout("sid-1", "csrf-1").After(login1)
	mock.ExpectLogin("app-password").After(logout).Reply(http.StatusOK, piholetest.Session("sid-2", "csrf-2"))

	mustSession(t, client)
	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}

	if got := mustSession(t, client).SID; got != "sid-2" {
		t.Fatalf("SID = %q, want sid-2", got)
	}
}

func TestUserAgentIsSentOnEveryRequest(t *testing.T) {
	t.Parallel()

	const agent = "pihole-exporter/v1.2.3"
	client, mock, _ := newMockedClient(t, WithUserAgent(agent))
	mock.ExpectLogin("app-password").Header("User-Agent", agent)
	mock.ExpectStats("/api/stats/summary", "sid-1", "csrf-1").Header("User-Agent", agent)
	mock.ExpectLogout("sid-1", "csrf-1").Header("User-Agent", agent)

	if _, err := client.GetJSONMap(context.Background(), "/stats/summary"); err != nil {
		t.Fatalf("GetJSONMap() error = %v", err)
	}
	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
}

func TestUserAgentDefaultsToTheApplicationName(t *testing.T) {
	t.Parallel()

	client, mock, _ := newMockedClient(t, WithUserAgent("  "))
	mock.ExpectLogin("app-password")

	mustSession(t, client)
}

func TestLogoutWithoutSessionMakesNoRequest(t *testing.T) {
	t.Parallel()

	client, _, _ := newMockedClient(t)

	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
}

func TestFailedLogoutDoesNotBlockReauth(t *testing.T) {
	t.Parallel()

	client, mock, now := newMockedClient(t, WithRefreshSkew(30*time.Second))
	login1 := mock.ExpectLogin("app-password")
	logout := mock.ExpectLogout("sid-1", "csrf-1").After(login1).Reply(http.StatusInternalServerError, nil)
	mock.ExpectLogin("app-password").After(logout).Reply(http.StatusOK, piholetest.Session("sid-2", "csrf-2"))

	mustSession(t, client)
	*now = now.Add(1771 * time.Second)

	if got := mustSession(t, client).SID; got != "sid-2" {
		t.Fatalf("SID = %q, want sid-2", got)
	}
}

func TestLogoutSkipsExpiredSession(t *testing.T) {
	t.Parallel()

	client, mock, now := newMockedClient(t)
	mock.ExpectLogin("app-password")

	mustSession(t, client)
	*now = now.Add(1801 * time.Second)

	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
}

func TestAddAuthHeaders(t *testing.T) {
	t.Parallel()

	client, mock, _ := newMockedClient(t)
	mock.ExpectLogin("app-password")

	req := httptest.NewRequest(http.MethodGet, "/api/stats/summary", nil)
	if err := client.AddAuthHeaders(context.Background(), req); err != nil {
		t.Fatalf("AddAuthHeaders() error = %v", err)
	}

	if got := req.Header.Get("X-FTL-SID"); got != "sid-1" {
		t.Fatalf("X-FTL-SID = %q, want sid-1", got)
	}
	if got := req.Header.Get("X-FTL-CSRF"); got != "csrf-1" {
		t.Fatalf("X-FTL-CSRF = %q, want csrf-1", got)
	}
}

func TestNewAuthClientFromEnv(t *testing.T) {
	t.Setenv(DefaultAppPasswordEnv, "from-env")

	mock := piholetest.New(t)
	mock.ExpectLogin("from-env")
	client, err := NewAuthClientFromEnv(piholetest.BaseURL, WithHTTPClient(mock.Client()))
	if err != nil {
		t.Fatalf("NewAuthClientFromEnv() error = %v", err)
	}

	mustSession(t, client)
}

func TestNewAuthClientRequiresPassword(t *testing.T) {
	t.Parallel()

	_, err := NewAuthClient("http://127.0.0.1:8080", "")
	if !errors.Is(err, ErrMissingPassword) {
		t.Fatalf("NewAuthClient() error = %v, want ErrMissingPassword", err)
	}
}

func TestSessionReturnsAPIErrorMessage(t *testing.T) {
	t.Parallel()

	mock := piholetest.New(t)
	mock.ExpectLogin("bad-password").Reply(http.StatusUnauthorized, piholetest.Unauthorized())
	client, err := NewAuthClient(piholetest.BaseURL, "bad-password", WithHTTPClient(mock.Client()))
	if err != nil {
		t.Fatalf("NewAuthClient() error = %v", err)
	}

	if _, err := client.Session(context.Background()); err == nil {
		t.Fatal("Session() error = nil, want error")
	}
}

func TestLiveAuth(t *testing.T) {
	if os.Getenv("PIHOLE_LIVE_TEST") != "1" {
		t.Skip("set PIHOLE_LIVE_TEST=1 to run against a real Pi-hole")
	}

	client, err := NewAuthClientFromEnv(envOrDefault("PIHOLE_BASE_URL", "http://192.168.0.2:9000"))
	if err != nil {
		t.Fatalf("NewAuthClientFromEnv() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := client.Session(ctx)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if session.SID == "" {
		t.Fatal("Session().SID is empty")
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
