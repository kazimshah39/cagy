package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGoogleCredentialRefresherRefreshesAndPreservesKeyringWrapper(t *testing.T) {
	var request url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if err := incoming.ParseForm(); err != nil {
			t.Fatal(err)
		}
		request = incoming.Form
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()

	raw := []byte(`{"token":{"access_token":"expired-access","refresh_token":"saved-refresh","token_type":"Bearer","expiry":"2026-09-18T00:00:00.000000Z"},"auth_method":"consumer","provider_field":"preserved"}`)
	wrapped := []byte("go-keyring-base64:" + base64.StdEncoding.EncodeToString(raw))
	refreshed, err := (GoogleCredentialRefresher{
		ClientID: "test-client", ClientSecret: "test-secret", TokenURL: server.URL,
		Client: server.Client(), Now: func() time.Time { return time.Date(2026, time.September, 19, 4, 0, 0, 0, time.UTC) },
	}).Refresh(context.Background(), wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(refreshed), "go-keyring-base64:") {
		t.Fatalf("wrapper was not preserved")
	}
	access, err := AccessToken(refreshed)
	if err != nil || access != "fresh-access" {
		t.Fatalf("access=%q err=%v", access, err)
	}
	decoded, _, err := decodeCredentialPayload(refreshed)
	if err != nil {
		t.Fatal(err)
	}
	defer Zero(decoded)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		t.Fatal(err)
	}
	if string(envelope["provider_field"]) != `"preserved"` {
		t.Fatalf("provider field was lost: %s", decoded)
	}
	var token map[string]string
	if err := json.Unmarshal(envelope["token"], &token); err != nil {
		t.Fatal(err)
	}
	if token["refresh_token"] != "saved-refresh" || token["expiry"] == "" {
		t.Fatalf("refreshed token fields are incomplete")
	}
	if request.Get("grant_type") != "refresh_token" || request.Get("refresh_token") != "saved-refresh" || request.Get("client_id") != "test-client" {
		t.Fatalf("unexpected refresh request fields")
	}
}

func TestGoogleCredentialRefresherReturnsSafeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"invalid_grant","error_description":"secret-provider-detail"}`))
	}))
	defer server.Close()
	credential := []byte(`{"token":{"access_token":"expired-access","refresh_token":"saved-refresh"},"auth_method":"consumer"}`)
	_, err := (GoogleCredentialRefresher{ClientID: "client", ClientSecret: "secret", TokenURL: server.URL, Client: server.Client()}).Refresh(context.Background(), credential)
	if err == nil {
		t.Fatal("expected refresh failure")
	}
	for _, secret := range []string{"saved-refresh", "secret-provider-detail", "invalid_grant"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}
