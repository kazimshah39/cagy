package accounts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	GoogleUserInfoEndpoint = "https://openidconnect.googleapis.com/v1/userinfo"
	maxCredentialBytes     = 1 << 20
	maxUserInfoBytes       = 64 << 10
)

type ProviderIdentity struct {
	Subject string
	Email   string
	Name    string
}

type IdentityResolver interface {
	Resolve(context.Context, []byte) (ProviderIdentity, error)
}

type HTTPIdentityResolver struct {
	Client   *http.Client
	Endpoint string
}

type credentialEnvelope struct {
	Token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type,omitempty"`
	} `json:"token"`
}

type userInfoResponse struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified *bool  `json:"email_verified"`
	Name          string `json:"name"`
}

func (r HTTPIdentityResolver) Resolve(ctx context.Context, credential []byte) (ProviderIdentity, error) {
	accessToken, err := AccessToken(credential)
	if err != nil {
		return ProviderIdentity{}, err
	}
	defer zeroStringBytes(&accessToken)
	endpoint := r.Endpoint
	if endpoint == "" {
		endpoint = GoogleUserInfoEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ProviderIdentity{}, errors.New("identity endpoint is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ProviderIdentity{}, errors.New("create identity request")
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	client := r.Client
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		client = &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		}
	} else {
		// Never inherit a caller's redirect policy for bearer-token requests.
		copy := *client
		client = &copy
		if client.Timeout <= 0 {
			client.Timeout = 15 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return ProviderIdentity{}, errors.New("verify account identity: request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxUserInfoBytes))
		return ProviderIdentity{}, fmt.Errorf("verify account identity: provider returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxUserInfoBytes+1))
	if err != nil {
		return ProviderIdentity{}, errors.New("verify account identity: read failed")
	}
	if len(body) > maxUserInfoBytes {
		return ProviderIdentity{}, errors.New("verify account identity: response is too large")
	}
	var info userInfoResponse
	if err := decodeSingleJSON(body, &info); err != nil {
		return ProviderIdentity{}, errors.New("verify account identity: invalid response")
	}
	info.Subject = strings.TrimSpace(info.Subject)
	info.Email = strings.TrimSpace(info.Email)
	info.Name = strings.Join(strings.Fields(info.Name), " ")
	if info.Subject == "" {
		return ProviderIdentity{}, errors.New("verify account identity: provider subject is missing")
	}
	if info.Email == "" || len(info.Email) > 320 || !emailPattern.MatchString(info.Email) {
		return ProviderIdentity{}, errors.New("verify account identity: provider email is invalid")
	}
	if info.EmailVerified != nil && !*info.EmailVerified {
		return ProviderIdentity{}, errors.New("verify account identity: provider email is not verified")
	}
	return ProviderIdentity{Subject: info.Subject, Email: info.Email, Name: info.Name}, nil
}

// AccessToken extracts only the short-lived access token from agy's opaque credential payload.
// The original credential bytes remain unchanged and are always stored verbatim.
func AccessToken(credential []byte) (string, error) {
	if len(credential) == 0 || len(credential) > maxCredentialBytes {
		return "", errors.New("credential payload has an invalid size")
	}
	payload := bytes.TrimSpace(credential)
	const keyringPrefix = "go-keyring-base64:"
	if bytes.HasPrefix(payload, []byte(keyringPrefix)) {
		decoded, err := base64.StdEncoding.DecodeString(string(payload[len(keyringPrefix):]))
		if err != nil {
			return "", errors.New("credential payload encoding is invalid")
		}
		defer clearBytes(decoded)
		payload = decoded
	} else if len(payload) > 0 && payload[0] != '{' {
		decoded, err := base64.StdEncoding.DecodeString(string(payload))
		if err != nil || len(decoded) == 0 || decoded[0] != '{' {
			clearBytes(decoded)
			return "", errors.New("credential payload format is unsupported")
		}
		defer clearBytes(decoded)
		payload = decoded
	}
	var envelope credentialEnvelope
	if err := decodeSingleJSON(payload, &envelope); err != nil {
		return "", errors.New("credential payload is invalid")
	}
	accessToken := strings.TrimSpace(envelope.Token.AccessToken)
	if accessToken == "" {
		return "", errors.New("credential payload has no access token")
	}
	if len(accessToken) > 16<<10 || strings.ContainsAny(accessToken, "\r\n\x00") {
		return "", errors.New("credential access token is invalid")
	}
	return accessToken, nil
}

// decodeSingleJSON accepts provider-added fields while rejecting concatenated
// or trailing JSON values. Credentials remain opaque outside the small fields
// needed for identity verification.
func decodeSingleJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

// Zero clears a temporary secret buffer on a best-effort basis.
func Zero(value []byte) { clearBytes(value) }

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func zeroStringBytes(value *string) {
	// Strings cannot be reliably zeroed in Go. Drop the reference as soon as the request is built.
	*value = ""
}
