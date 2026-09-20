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
	default9RouterURL   = "http://127.0.0.1:20128"
	routerHealthPath    = "/api/health"
	routerCheckTimeout  = 2 * time.Second
	maxRouterStatusBody = 32 << 10
)

// routerHealth is the only public 9Router surface cagy reads. Cagy treats the
// router as an external prerequisite: it does not inspect or operate its MITM
// process, certificates, DNS, provider accounts, or credentials.
type routerHealth struct {
	OK bool `json:"ok"`
}

func (a *App) routerURL() string {
	if a.getenv != nil {
		if value := strings.TrimRight(strings.TrimSpace(a.getenv("CAGY_ROUTER_URL")), "/"); value != "" {
			return value
		}
	}
	return default9RouterURL
}

func (a *App) checkRouter(ctx context.Context) error {
	if a.routerCheck != nil {
		return a.routerCheck(ctx)
	}
	return check9RouterHealth(ctx, a.routerURL())
}

func check9RouterHealth(ctx context.Context, baseURL string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return fmt.Errorf("9Router URL is empty")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("9Router URL is invalid")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("9Router URL must use a local host")
	}
	requestCtx, cancel := context.WithTimeout(ctx, routerCheckTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, baseURL+routerHealthPath, nil)
	if err != nil {
		return fmt.Errorf("build 9Router health request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		return fmt.Errorf("9Router is not reachable at %s; start 9Router before using cagy", baseURL)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("9Router health check returned HTTP %d; check the 9Router dashboard", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRouterStatusBody+1))
	if err != nil {
		return fmt.Errorf("read 9Router health: %w", err)
	}
	if len(body) > maxRouterStatusBody {
		return fmt.Errorf("9Router health response is too large")
	}
	var health routerHealth
	if err := json.Unmarshal(body, &health); err != nil {
		return fmt.Errorf("9Router health response is invalid")
	}
	if !health.OK {
		return fmt.Errorf("9Router health check failed; check the 9Router dashboard")
	}
	return nil
}
