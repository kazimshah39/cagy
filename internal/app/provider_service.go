package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultProviderServiceURL   = "http://127.0.0.1:20128"
	providerServiceHealthPath   = "/api/health"
	providerServiceCheckTimeout = 2 * time.Second
	maxProviderServiceBody      = 32 << 10
)

// providerServiceHealth is the only external service response Herdr Tandem reads.
// The service remains responsible for provider access and account fallback.
type providerServiceHealth struct {
	OK bool `json:"ok"`
}

func (a *App) providerServiceURL() string {
	if a.getenv != nil {
		if value := strings.TrimRight(strings.TrimSpace(a.getenv("HERDR_TANDEM_PROVIDER_SERVICE_URL")), "/"); value != "" {
			return value
		}
	}
	return defaultProviderServiceURL
}

func (a *App) checkProviderService(ctx context.Context) error {
	if a.providerServiceCheck != nil {
		return a.providerServiceCheck(ctx)
	}
	return checkProviderServiceHealth(ctx, a.providerServiceURL())
}

func checkProviderServiceHealth(ctx context.Context, baseURL string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return fmt.Errorf("provider service URL is empty")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("provider service URL is invalid")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("provider service URL must use a local host")
	}
	requestCtx, cancel := context.WithTimeout(ctx, providerServiceCheckTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, baseURL+providerServiceHealthPath, nil)
	if err != nil {
		return fmt.Errorf("build provider service health request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		return fmt.Errorf("provider service is not reachable at %s; start it before using herdr-tandem", baseURL)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("provider service health check returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProviderServiceBody+1))
	if err != nil {
		return fmt.Errorf("read provider service health: %w", err)
	}
	if len(body) > maxProviderServiceBody {
		return fmt.Errorf("provider service health response is too large")
	}
	var health providerServiceHealth
	if err := json.Unmarshal(body, &health); err != nil {
		return fmt.Errorf("provider service health response is invalid")
	}
	if !health.OK {
		return fmt.Errorf("provider service health check failed")
	}
	return nil
}
