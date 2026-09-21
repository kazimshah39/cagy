package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func providerServiceHealthServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != providerServiceHealthPath {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("herdr-tandem must not send provider credentials, got %q", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestCheckProviderServiceHealthAcceptsPublicHealthEndpoint(t *testing.T) {
	server := providerServiceHealthServer(t, http.StatusOK, `{"ok":true}`)
	defer server.Close()
	if err := checkProviderServiceHealth(context.Background(), server.URL); err != nil {
		t.Fatal(err)
	}
}

func TestCheckProviderServiceHealthRejectsUnhealthyResponses(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"unhealthy", `{"ok":false}`, "health check failed"},
		{"missing health flag", `{}`, "health check failed"},
		{"invalid payload", `not-json`, "health response is invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := providerServiceHealthServer(t, http.StatusOK, test.body)
			defer server.Close()
			err := checkProviderServiceHealth(context.Background(), server.URL)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCheckProviderServiceHealthRejectsUnavailableOrUnexpectedHTTP(t *testing.T) {
	if err := checkProviderServiceHealth(context.Background(), "http://127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("error=%v", err)
	}
	server := providerServiceHealthServer(t, http.StatusUnauthorized, `{}`)
	defer server.Close()
	if err := checkProviderServiceHealth(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("error=%v", err)
	}
}

func TestCheckProviderServiceHealthRejectsNonLocalOrAmbiguousBaseURLs(t *testing.T) {
	for _, raw := range []string{
		"https://service.example.test",
		"http://127.0.0.1/nested",
		"http://127.0.0.1?next=/api/health",
		"http://user@127.0.0.1:20128",
	} {
		if err := checkProviderServiceHealth(context.Background(), raw); err == nil {
			t.Fatalf("URL %q was accepted", raw)
		}
	}
}

func TestProviderServiceURLUsesExplicitOverride(t *testing.T) {
	app := New(fakeRunner{}, nil, nil)
	app.getenv = func(key string) string {
		if key == "HERDR_TANDEM_PROVIDER_SERVICE_URL" {
			return "http://service.test///"
		}
		return ""
	}
	if got := app.providerServiceURL(); got != "http://service.test" {
		t.Fatalf("url=%q", got)
	}
}
