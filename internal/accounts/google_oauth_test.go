package accounts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGoogleOAuthLoginUsesLoopbackPKCEAndReturnsAgyCredential(t *testing.T) {
	var verifierSeen bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		verifierSeen = r.Form.Get("code_verifier") != ""
		if r.Form.Get("code") != "test-code" || !verifierSeen {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-value","refresh_token":"refresh-value","token_type":"Bearer","expires_in":3600}`))
	}))
	defer provider.Close()

	login := GoogleOAuthLogin{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		AuthURL:      provider.URL + "/auth",
		TokenURL:     provider.URL + "/token",
		HTTPClient:   provider.Client(),
		Timeout:      5 * time.Second,
		Now:          func() time.Time { return time.Date(2026, time.September, 19, 3, 0, 0, 0, time.UTC) },
	}
	credential, err := login.Login(context.Background(), func(target string) error {
		parsed, parseErr := url.Parse(target)
		if parseErr != nil {
			return parseErr
		}
		query := parsed.Query()
		if query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" {
			t.Fatalf("PKCE was not enabled: %s", target)
		}
		if query.Get("access_type") != "offline" || query.Get("prompt") != "consent" {
			t.Fatalf("reusable consent was not requested: %s", target)
		}
		callback, parseErr := url.Parse(query.Get("redirect_uri"))
		if parseErr != nil {
			return parseErr
		}
		if callback.Scheme != "http" || callback.Hostname() != "localhost" || callback.Path != "/oauth-callback" {
			t.Fatalf("unsafe callback=%s", callback)
		}
		values := callback.Query()
		values.Set("state", query.Get("state"))
		values.Set("code", "test-code")
		callback.RawQuery = values.Encode()
		response, getErr := http.Get(callback.String()) // #nosec G107 -- test-only loopback callback.
		if getErr != nil {
			return getErr
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer Zero(credential)
	if !verifierSeen {
		t.Fatal("token exchange did not include the PKCE verifier")
	}
	accessToken, err := AccessToken(credential)
	if err != nil || accessToken != "access-value" {
		t.Fatalf("access token=%q err=%v", accessToken, err)
	}
	var envelope agyCredentialEnvelope
	if err := json.Unmarshal(credential, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Token.RefreshToken != "refresh-value" || envelope.AuthMethod != "consumer" || !strings.HasSuffix(envelope.Token.Expiry, "Z") {
		t.Fatalf("credential shape=%+v", envelope)
	}
}

func TestGoogleOAuthLoginRejectsMismatchedStateBeforeExchange(t *testing.T) {
	login := GoogleOAuthLogin{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		AuthURL:      "https://accounts.example.test/auth",
		TokenURL:     "https://accounts.example.test/token",
		Timeout:      5 * time.Second,
	}
	_, err := login.Login(context.Background(), func(target string) error {
		parsed, parseErr := url.Parse(target)
		if parseErr != nil {
			return parseErr
		}
		callback, parseErr := url.Parse(parsed.Query().Get("redirect_uri"))
		if parseErr != nil {
			return parseErr
		}
		values := callback.Query()
		values.Set("state", "wrong-state")
		values.Set("code", "must-not-exchange")
		callback.RawQuery = values.Encode()
		response, getErr := http.Get(callback.String()) // #nosec G107 -- test-only loopback callback.
		if getErr != nil {
			return getErr
		}
		_ = response.Body.Close()
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "state did not match") {
		t.Fatalf("error=%v", err)
	}
}

func TestGoogleOAuthClientConfigUsesEnvironment(t *testing.T) {
	t.Setenv(googleClientIDEnv, "env-client")
	t.Setenv(googleClientSecretEnv, "env-secret")

	clientID, clientSecret, err := googleOAuthClientConfig("", "")
	if err != nil {
		t.Fatal(err)
	}
	if clientID != "env-client" || clientSecret != "env-secret" {
		t.Fatalf("config=%q/%q", clientID, clientSecret)
	}
}

func TestGoogleOAuthClientConfigRequiresCredentials(t *testing.T) {
	t.Setenv(googleClientIDEnv, "")
	t.Setenv(googleClientSecretEnv, "")

	_, _, err := googleOAuthClientConfig("", "")
	if err == nil || !strings.Contains(err.Error(), googleClientIDEnv) || !strings.Contains(err.Error(), googleClientSecretEnv) {
		t.Fatalf("error=%v", err)
	}
}
