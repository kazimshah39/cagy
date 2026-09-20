package accounts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

var (
	ErrCredentialNeedsLogin         = errors.New("account credential needs login")
	ErrCredentialRefreshUnavailable = errors.New("account credential refresh is temporarily unavailable")
)

// CredentialRefreshUnavailable reports process-wide or transport failures that
// should stop an automatic rotation pass without penalizing every stored
// account. Authentication rejection of one refresh token is intentionally not
// included; callers handle that as an account-specific needs-login failure.
func CredentialRefreshUnavailable(err error) bool {
	return errors.Is(err, ErrCredentialRefreshUnavailable)
}

type credentialEncoding uint8

const (
	credentialJSON credentialEncoding = iota
	credentialBase64
	credentialKeyringBase64
)

// GoogleCredentialRefresher refreshes one stored agy OAuth credential without
// starting agy or opening a browser. The input wrapper is preserved so AGM
// imports and credentials created by cagy remain compatible.
type GoogleCredentialRefresher struct {
	Client       *http.Client
	ClientID     string
	ClientSecret string
	TokenURL     string
	Now          func() time.Time
}

func (r GoogleCredentialRefresher) Refresh(ctx context.Context, credential []byte) ([]byte, error) {
	payload, encoding, err := decodeCredentialPayload(credential)
	if err != nil {
		return nil, err
	}
	defer clearBytes(payload)

	var envelope map[string]json.RawMessage
	if err := decodeSingleJSON(payload, &envelope); err != nil {
		return nil, errors.New("credential payload is invalid")
	}
	var tokenFields map[string]json.RawMessage
	if err := json.Unmarshal(envelope["token"], &tokenFields); err != nil {
		return nil, errors.New("credential token is invalid")
	}
	readString := func(name string) string {
		var value string
		_ = json.Unmarshal(tokenFields[name], &value)
		return strings.TrimSpace(value)
	}
	refreshToken := readString("refresh_token")
	if refreshToken == "" {
		return nil, fmt.Errorf("%w: credential has no reusable refresh token", ErrCredentialNeedsLogin)
	}

	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	clientID, clientSecret, err := googleOAuthClientConfig(r.ClientID, r.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("%w: OAuth client configuration is unavailable", ErrCredentialRefreshUnavailable)
	}
	config := oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL:  firstNonEmptyString(r.TokenURL, googleTokenURL),
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	refreshCtx := context.WithValue(ctx, oauth2.HTTPClient, client)
	fresh, err := config.TokenSource(refreshCtx, &oauth2.Token{
		AccessToken:  readString("access_token"),
		TokenType:    firstNonEmptyString(readString("token_type"), "Bearer"),
		RefreshToken: refreshToken,
		// Force an explicit refresh. Stored AGM snapshots commonly contain an
		// expired access token even though their refresh token is still valid.
		Expiry: now().Add(-time.Minute),
	}).Token()
	if err != nil {
		var retrieveError *oauth2.RetrieveError
		if errors.As(err, &retrieveError) {
			switch strings.ToLower(strings.TrimSpace(retrieveError.ErrorCode)) {
			case "invalid_grant":
				return nil, fmt.Errorf("%w: Google rejected the reusable credential", ErrCredentialNeedsLogin)
			case "invalid_client", "unauthorized_client":
				return nil, fmt.Errorf("%w: Google rejected the OAuth client", ErrCredentialRefreshUnavailable)
			default:
				return nil, errors.New("refresh Google account credential failed")
			}
		}
		return nil, fmt.Errorf("%w: Google token service could not be reached", ErrCredentialRefreshUnavailable)
	}
	if strings.TrimSpace(fresh.AccessToken) == "" {
		return nil, errors.New("refreshed Google account credential has no access token")
	}
	if strings.TrimSpace(fresh.RefreshToken) == "" {
		fresh.RefreshToken = refreshToken
	}
	if fresh.Expiry.IsZero() {
		fresh.Expiry = now().Add(time.Hour)
	}

	setString := func(name, value string) error {
		encoded, encodeErr := json.Marshal(value)
		if encodeErr == nil {
			tokenFields[name] = encoded
		}
		return encodeErr
	}
	if err := setString("access_token", fresh.AccessToken); err != nil {
		return nil, errors.New("encode refreshed access token")
	}
	if err := setString("refresh_token", fresh.RefreshToken); err != nil {
		return nil, errors.New("encode refreshed refresh token")
	}
	if err := setString("token_type", firstNonEmptyString(fresh.TokenType, "Bearer")); err != nil {
		return nil, errors.New("encode refreshed token type")
	}
	if err := setString("expiry", fresh.Expiry.UTC().Format("2006-01-02T15:04:05.000000Z")); err != nil {
		return nil, errors.New("encode refreshed token expiry")
	}
	tokenJSON, err := json.Marshal(tokenFields)
	if err != nil {
		return nil, errors.New("encode refreshed credential token")
	}
	envelope["token"] = tokenJSON
	refreshedPayload, err := json.Marshal(envelope)
	if err != nil {
		return nil, errors.New("encode refreshed credential")
	}
	return encodeCredentialPayload(refreshedPayload, encoding), nil
}

func decodeCredentialPayload(credential []byte) ([]byte, credentialEncoding, error) {
	payload := bytes.TrimSpace(credential)
	if len(payload) == 0 || len(payload) > maxCredentialBytes {
		return nil, credentialJSON, errors.New("credential payload has an invalid size")
	}
	const prefix = "go-keyring-base64:"
	if bytes.HasPrefix(payload, []byte(prefix)) {
		decoded, err := base64.StdEncoding.DecodeString(string(payload[len(prefix):]))
		if err != nil || len(decoded) == 0 || len(decoded) > maxCredentialBytes || !json.Valid(decoded) {
			clearBytes(decoded)
			return nil, credentialJSON, errors.New("credential payload encoding is invalid")
		}
		return decoded, credentialKeyringBase64, nil
	}
	if payload[0] == '{' {
		if !json.Valid(payload) {
			return nil, credentialJSON, errors.New("credential payload is invalid")
		}
		return append([]byte(nil), payload...), credentialJSON, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(string(payload))
	if err != nil || len(decoded) == 0 || len(decoded) > maxCredentialBytes || !json.Valid(decoded) {
		clearBytes(decoded)
		return nil, credentialJSON, errors.New("credential payload format is unsupported")
	}
	return decoded, credentialBase64, nil
}

func encodeCredentialPayload(payload []byte, encoding credentialEncoding) []byte {
	switch encoding {
	case credentialKeyringBase64:
		return []byte("go-keyring-base64:" + base64.StdEncoding.EncodeToString(payload))
	case credentialBase64:
		return []byte(base64.StdEncoding.EncodeToString(payload))
	default:
		return append([]byte(nil), payload...)
	}
}
