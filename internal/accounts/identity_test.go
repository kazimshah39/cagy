package accounts

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func credentialFixture(token string) []byte {
	return []byte(`{"token":{"access_token":"` + token + `","refresh_token":"must-never-leak"}}`)
}

func TestAccessTokenSupportsAgyCredentialEncodings(t *testing.T) {
	raw := credentialFixture("access-value")
	cases := [][]byte{
		raw,
		[]byte(base64.StdEncoding.EncodeToString(raw)),
		[]byte("go-keyring-base64:" + base64.StdEncoding.EncodeToString(raw)),
	}
	for _, value := range cases {
		token, err := AccessToken(value)
		if err != nil || token != "access-value" {
			t.Fatalf("token=%q err=%v", token, err)
		}
	}
	for _, value := range [][]byte{nil, []byte("not-a-credential"), []byte(`{"token":{}}`)} {
		if _, err := AccessToken(value); err == nil {
			t.Fatalf("expected rejection for %q", value)
		}
	}
}

func TestAccessTokenRejectsTrailingJSONValue(t *testing.T) {
	credential := []byte(`{"token":{"access_token":"secret"}} {"extra":true}`)
	if _, err := AccessToken(credential); err == nil || !strings.Contains(err.Error(), "credential payload is invalid") {
		t.Fatalf("error=%v", err)
	}
}

func TestIdentityResolverVerifiesUserInfoAndDoesNotLeakSecrets(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer access-value" {
			t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"sub":"provider-subject","email":"owner@example.com","email_verified":true,"name":" Test  Owner "}`))
	}))
	defer server.Close()
	resolver := HTTPIdentityResolver{Client: server.Client(), Endpoint: server.URL}
	identity, err := resolver.Resolve(context.Background(), credentialFixture("access-value"))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "provider-subject" || identity.Email != "owner@example.com" || identity.Name != "Test Owner" {
		t.Fatalf("identity=%+v", identity)
	}
}

func TestIdentityResolverSanitizesProviderErrors(t *testing.T) {
	secret := "access-secret-123"
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"` + secret + `","refresh_token":"also-secret"}`))
	}))
	defer server.Close()
	resolver := HTTPIdentityResolver{Client: server.Client(), Endpoint: server.URL}
	_, err := resolver.Resolve(context.Background(), credentialFixture(secret))
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "also-secret") {
		t.Fatalf("unsafe error=%v", err)
	}
}

func TestIdentityResolverRejectsRedirectMalformedAndUnverified(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "redirect", handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://example.com", http.StatusFound)
		}},
		{name: "malformed", handler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not-json")) }},
		{name: "missing subject", handler: func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"email":"a@example.com"}`)) }},
		{name: "unverified", handler: func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"sub":"x","email":"a@example.com","email_verified":false}`))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(test.handler)
			defer server.Close()
			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			resolver := HTTPIdentityResolver{Client: client, Endpoint: server.URL}
			if _, err := resolver.Resolve(context.Background(), credentialFixture("token")); err == nil {
				t.Fatal("expected identity rejection")
			}
		})
	}
}

func TestIdentityResolverRejectsTrailingJSONValue(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"sub":"subject","email":"user@example.com"} {"extra":true}`))
	}))
	defer server.Close()
	resolver := HTTPIdentityResolver{Client: server.Client(), Endpoint: server.URL}
	credential := []byte(`{"token":{"access_token":"secret"}}`)
	if _, err := resolver.Resolve(context.Background(), credential); err == nil || !strings.Contains(err.Error(), "invalid response") {
		t.Fatalf("error=%v", err)
	}
}

func TestIdentityResolverHonorsContextTimeout(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := (HTTPIdentityResolver{Client: server.Client(), Endpoint: server.URL}).Resolve(ctx, credentialFixture("token"))
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("timeout err=%v ctx=%v", err, ctx.Err())
	}
}
