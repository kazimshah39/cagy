package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func routerHealthServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != routerHealthPath {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("x-9r-cli-token"); got != "" {
			t.Fatalf("cagy must not send a 9Router CLI token, got %q", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestCheck9RouterHealthAcceptsPublicHealthEndpoint(t *testing.T) {
	server := routerHealthServer(t, http.StatusOK, `{"ok":true}`)
	defer server.Close()
	if err := check9RouterHealth(context.Background(), server.URL); err != nil {
		t.Fatal(err)
	}
}

func TestCheck9RouterHealthRejectsUnhealthyResponses(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"unhealthy", `{"ok":false}`, "health check failed"},
		{"missing health flag", `{}`, "health check failed"},
		{"invalid payload", `not-json`, "health response is invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := routerHealthServer(t, http.StatusOK, test.body)
			defer server.Close()
			err := check9RouterHealth(context.Background(), server.URL)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCheck9RouterHealthRejectsUnavailableOrUnexpectedHTTP(t *testing.T) {
	if err := check9RouterHealth(context.Background(), "http://127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("error=%v", err)
	}
	server := routerHealthServer(t, http.StatusUnauthorized, `{}`)
	defer server.Close()
	if err := check9RouterHealth(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("error=%v", err)
	}
}

func TestCheck9RouterHealthRejectsNonLocalOrAmbiguousBaseURLs(t *testing.T) {
	for _, raw := range []string{
		"https://router.example.test",
		"http://127.0.0.1/router",
		"http://127.0.0.1?next=/api/health",
		"http://user@127.0.0.1:20128",
	} {
		if err := check9RouterHealth(context.Background(), raw); err == nil {
			t.Fatalf("URL %q was accepted", raw)
		}
	}
}

func TestRouterURLUsesExplicitOverride(t *testing.T) {
	app := New(fakeRunner{}, nil, nil)
	app.getenv = func(key string) string {
		if key == "CAGY_ROUTER_URL" {
			return "http://router.test///"
		}
		return ""
	}
	if got := app.routerURL(); got != "http://router.test" {
		t.Fatalf("url=%q", got)
	}
}
