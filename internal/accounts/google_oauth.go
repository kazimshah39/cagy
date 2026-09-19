package accounts

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	googleClientIDEnv     = "CAGY_GOOGLE_CLIENT_ID"
	googleClientSecretEnv = "CAGY_GOOGLE_CLIENT_SECRET"
	googleAuthURL         = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL        = "https://oauth2.googleapis.com/token"
)

var agyGoogleScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
	"https://www.googleapis.com/auth/aicode",
	"openid",
}

type GoogleOAuthLogin struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	HTTPClient   *http.Client
	Listen       func(string, string) (net.Listener, error)
	Timeout      time.Duration
	Now          func() time.Time
}

type oauthCallback struct {
	code string
	err  error
}

type agyCredentialEnvelope struct {
	Token struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		Expiry       string `json:"expiry"`
	} `json:"token"`
	AuthMethod string `json:"auth_method"`
}

// Login runs an explicit browser OAuth flow and returns an agy-compatible
// credential snapshot. It writes only to cagy's local vault; it never reads or
// changes the canonical macOS Keychain item.
func (l GoogleOAuthLogin) Login(ctx context.Context, openURL func(string) error) ([]byte, error) {
	if openURL == nil {
		return nil, errors.New("browser opener is unavailable")
	}
	listen := l.Listen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start OAuth callback listener: %w", err)
	}
	defer listener.Close()

	timeout := l.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	loginCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	state, err := randomOAuthState()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()
	_, callbackPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil || callbackPort == "" {
		return nil, errors.New("resolve OAuth callback port")
	}
	redirectURI := "http://localhost:" + callbackPort + "/oauth-callback"
	clientID, clientSecret, err := googleOAuthClientConfig(l.ClientID, l.ClientSecret)
	if err != nil {
		return nil, err
	}
	config := oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURI,
		Scopes:       append([]string(nil), agyGoogleScopes...),
		Endpoint: oauth2.Endpoint{
			AuthURL:   firstNonEmptyString(l.AuthURL, googleAuthURL),
			TokenURL:  firstNonEmptyString(l.TokenURL, googleTokenURL),
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}

	callbackCh := make(chan oauthCallback, 1)
	deliver := func(result oauthCallback) {
		select {
		case callbackCh <- result:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte("Method not allowed"))
			return
		}
		gotState := r.URL.Query().Get("state")
		if len(gotState) != len(state) || subtle.ConstantTimeCompare([]byte(gotState), []byte(state)) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("Invalid OAuth state. Return to the terminal and try again."))
			deliver(oauthCallback{err: errors.New("OAuth callback state did not match")})
			return
		}
		if providerErr := strings.TrimSpace(r.URL.Query().Get("error")); providerErr != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "Login was not completed: %s", html.EscapeString(providerErr))
			deliver(oauthCallback{err: fmt.Errorf("Google login was not completed: %s", providerErr)})
			return
		}
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("Missing authorization code. Return to the terminal and try again."))
			deliver(oauthCallback{err: errors.New("OAuth callback did not include an authorization code")})
			return
		}
		_, _ = w.Write([]byte("<!doctype html><html><body style=\"font-family:system-ui;padding:3rem;text-align:center\"><h1>Account added</h1><p>You can close this tab and return to cagy.</p></body></html>"))
		deliver(oauthCallback{code: code})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			deliver(oauthCallback{err: fmt.Errorf("OAuth callback server failed: %w", serveErr)})
		}
	}()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		<-serveDone
	}()

	authURL := config.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("include_granted_scopes", "true"),
	)
	if err := openURL(authURL); err != nil {
		return nil, fmt.Errorf("open Google login: %w", err)
	}

	var callback oauthCallback
	select {
	case callback = <-callbackCh:
	case <-loginCtx.Done():
		if errors.Is(loginCtx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("Google login timed out")
		}
		return nil, loginCtx.Err()
	}
	if callback.err != nil {
		return nil, callback.err
	}

	exchangeCtx := loginCtx
	if l.HTTPClient != nil {
		exchangeCtx = context.WithValue(exchangeCtx, oauth2.HTTPClient, l.HTTPClient)
	}
	token, err := config.Exchange(exchangeCtx, callback.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, errors.New("exchange Google authorization code")
	}
	if strings.TrimSpace(token.AccessToken) == "" || strings.TrimSpace(token.RefreshToken) == "" {
		return nil, errors.New("Google login did not return reusable account credentials")
	}
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	expiry := token.Expiry
	if expiry.IsZero() {
		expiry = now().Add(time.Hour)
	}
	var credential agyCredentialEnvelope
	credential.Token.AccessToken = token.AccessToken
	credential.Token.TokenType = firstNonEmptyString(token.TokenType, "Bearer")
	credential.Token.RefreshToken = token.RefreshToken
	credential.Token.Expiry = expiry.UTC().Format("2006-01-02T15:04:05.000000Z")
	credential.AuthMethod = "consumer"
	encoded, err := json.Marshal(credential)
	if err != nil {
		return nil, errors.New("encode account credential")
	}
	return encoded, nil
}

func googleOAuthClientConfig(clientID, clientSecret string) (string, string, error) {
	clientID = firstNonEmptyString(clientID, os.Getenv(googleClientIDEnv))
	clientSecret = firstNonEmptyString(clientSecret, os.Getenv(googleClientSecretEnv))
	if clientID == "" || clientSecret == "" {
		return "", "", fmt.Errorf("Google OAuth is not configured; set %s and %s before running cagy accounts add or refreshing accounts", googleClientIDEnv, googleClientSecretEnv)
	}
	return clientID, clientSecret, nil
}

func randomOAuthState() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("create OAuth state")
	}
	return hex.EncodeToString(value[:]), nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
