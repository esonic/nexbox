package agent

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// startTestProxy starts a ContainerProxy on a random port and returns it.
func startTestProxy(t *testing.T, realAPIKey string, extra []string) *ContainerProxy {
	t.Helper()
	p := NewContainerProxy("127.0.0.1:0", realAPIKey, extra)
	if err := p.Start(); err != nil {
		t.Fatalf("proxy Start: %v", err)
	}
	t.Cleanup(p.Stop)
	return p
}

// proxyClient returns an http.Client that routes all traffic through the proxy.
func proxyClient(t *testing.T, proxyAddr string) *http.Client {
	t.Helper()
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// NewContainerProxy defaults
// ---------------------------------------------------------------------------

func TestNewContainerProxy_DefaultAllowedHosts(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", "key", nil)
	for _, h := range defaultAllowedHosts {
		if !p.isAllowedConnect(h) {
			t.Errorf("expected %s to be allowed by default", h)
		}
	}
}

func TestNewContainerProxy_ExtraAllowedHosts(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", "key", []string{"example.com", ""})
	if !p.isAllowedConnect("example.com") {
		t.Error("expected example.com to be allowed (extra host)")
	}
	// Empty string must not panic
	if p.isAllowedConnect("") {
		t.Error("empty host must not be allowed")
	}
}

func TestNewContainerProxy_BlockedByDefault(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", "key", nil)
	// Arbitrary external hosts must still be blocked; marketplace connectors are allowed.
	blocked := []string{"evil.com", "google.com", "attacker.io"}
	for _, h := range blocked {
		if p.isAllowedConnect(h) {
			t.Errorf("expected %s to be blocked by default", h)
		}
	}
	// Known-good marketplace/dev hosts must be allowed by default.
	allowed := []string{"github.com", "api.github.com", "registry.npmjs.org", "mcp.slack.com", "mcp.stripe.com"}
	for _, h := range allowed {
		if !p.isAllowedConnect(h) {
			t.Errorf("expected %s to be allowed by default (marketplace connector)", h)
		}
	}
}

// ---------------------------------------------------------------------------
// isAllowedConnect — host stripping
// ---------------------------------------------------------------------------

func TestIsAllowedConnect_StripsPort(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", "key", []string{"example.com"})
	if !p.isAllowedConnect("example.com:443") {
		t.Error("should allow example.com:443 (strips port)")
	}
	if !p.isAllowedConnect("example.com:8080") {
		t.Error("should allow example.com:8080 (strips port)")
	}
}

func TestIsAllowedConnect_CaseInsensitive(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", "key", []string{"Example.Com"})
	if !p.isAllowedConnect("example.com") {
		t.Error("host matching must be case-insensitive")
	}
	if !p.isAllowedConnect("EXAMPLE.COM") {
		t.Error("host matching must be case-insensitive (upper)")
	}
}

// ---------------------------------------------------------------------------
// Session management
// ---------------------------------------------------------------------------

func TestSessionRegistration(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", "key", nil)

	p.RegisterSession("tok-aaa", "proj-1")
	p.RegisterSession("tok-bbb", "proj-2")

	if p.SessionCount() != 2 {
		t.Fatalf("expected 2 sessions, got %d", p.SessionCount())
	}

	id, ok := p.lookupSession("tok-aaa")
	if !ok || id != "proj-1" {
		t.Fatalf("expected proj-1 for tok-aaa, got %q ok=%v", id, ok)
	}
}

func TestSessionRevocation(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", "key", nil)
	p.RegisterSession("tok-xyz", "proj-x")
	p.RevokeSession("tok-xyz")

	if p.SessionCount() != 0 {
		t.Fatalf("expected 0 sessions after revoke, got %d", p.SessionCount())
	}
	_, ok := p.lookupSession("tok-xyz")
	if ok {
		t.Fatal("revoked token must not be found")
	}
}

func TestSessionRevocation_NonExistent(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", "key", nil)
	p.RevokeSession("does-not-exist") // must not panic
}

func TestLookupSession_Unknown(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", "key", nil)
	_, ok := p.lookupSession("unknown")
	if ok {
		t.Fatal("unknown token must not be found")
	}
}

// ---------------------------------------------------------------------------
// isAnthropicRelay — detection heuristic
// ---------------------------------------------------------------------------

func TestIsAnthropicRelay_DetectsOwnPort(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:9876", "key", nil)
	req, _ := http.NewRequest("POST", "http://127.0.0.1:9876/v1/messages", nil)
	if !p.isAnthropicRelay(req) {
		t.Error("request on proxy port with /v1/ path should be detected as relay")
	}
}

func TestIsAnthropicRelay_WrongPath(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:9876", "key", nil)
	req, _ := http.NewRequest("GET", "http://127.0.0.1:9876/healthz", nil)
	if p.isAnthropicRelay(req) {
		t.Error("/healthz must not be detected as relay")
	}
}

func TestIsAnthropicRelay_WrongPort(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:9876", "key", nil)
	req, _ := http.NewRequest("POST", "http://127.0.0.1:1234/v1/messages", nil)
	if p.isAnthropicRelay(req) {
		t.Error("request on different port must not be detected as relay")
	}
}

// ---------------------------------------------------------------------------
// Proxy server — start/stop
// ---------------------------------------------------------------------------

func TestProxy_Start(t *testing.T) {
	p := startTestProxy(t, "test-key", nil)
	if p.Addr() == "" {
		t.Fatal("proxy Addr must not be empty after Start")
	}
}

func TestProxy_AddrUpdatedAfterBind(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", "key", nil)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Stop()
	// Port 0 should be replaced with the actual bound port
	if strings.HasSuffix(p.Addr(), ":0") {
		t.Fatalf("Addr should not be :0 after Start, got %s", p.Addr())
	}
}

func TestProxy_StopIdempotent(t *testing.T) {
	p := startTestProxy(t, "key", nil)
	p.Stop()
	p.Stop() // second call must not panic
}

// ---------------------------------------------------------------------------
// Relay handler — HTTP through proxy
// ---------------------------------------------------------------------------

func TestRelay_InvalidSessionToken(t *testing.T) {
	p := startTestProxy(t, "real-key", nil)
	client := proxyClient(t, p.Addr())

	// Send a relay-style request (to the proxy's own port) with a bad token.
	reqURL := fmt.Sprintf("http://%s/v1/messages", p.Addr())
	req, _ := http.NewRequest("POST", reqURL, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer invalid-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid token, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "authentication_error") {
		t.Errorf("expected authentication_error in body, got: %s", body)
	}
}

func TestRelay_NoAuthorizationHeader(t *testing.T) {
	p := startTestProxy(t, "real-key", nil)
	client := proxyClient(t, p.Addr())

	reqURL := fmt.Sprintf("http://%s/v1/messages", p.Addr())
	req, _ := http.NewRequest("POST", reqURL, nil)
	// No Authorization header

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing token, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Egress whitelist — CONNECT tunnel enforcement
// ---------------------------------------------------------------------------

func TestEgress_BlockedCONNECT(t *testing.T) {
	p := startTestProxy(t, "key", nil)
	client := proxyClient(t, p.Addr())

	// Attempt HTTPS CONNECT to a non-whitelisted host.
	// The Go HTTP client does CONNECT automatically for https:// via a proxy.
	resp, err := client.Get("https://google.com")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error (connection refused) for non-whitelisted CONNECT")
	}
	// CONNECT rejection manifests as a transport error or 403
}

func TestEgress_BlockedAnthropicCONNECT(t *testing.T) {
	// api.anthropic.com must not be reachable via CONNECT tunnel;
	// containers must use the relay (ANTHROPIC_BASE_URL) instead.
	p := startTestProxy(t, "key", nil)
	client := proxyClient(t, p.Addr())

	resp, err := client.Get("https://api.anthropic.com/v1/models")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected CONNECT to api.anthropic.com to be blocked")
	}
}

// ---------------------------------------------------------------------------
// Concurrent session operations
// ---------------------------------------------------------------------------

func TestSessionConcurrency(t *testing.T) {
	p := startTestProxy(t, "key", nil)
	done := make(chan struct{})

	// Register/revoke sessions concurrently from multiple goroutines
	for i := 0; i < 50; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			tok := fmt.Sprintf("tok-%d", i)
			proj := fmt.Sprintf("proj-%d", i)
			p.RegisterSession(tok, proj)
			_, _ = p.lookupSession(tok)
			p.RevokeSession(tok)
		}(i)
	}
	for i := 0; i < 50; i++ {
		<-done
	}

	if p.SessionCount() != 0 {
		t.Fatalf("expected 0 sessions after concurrent register+revoke, got %d", p.SessionCount())
	}
}
