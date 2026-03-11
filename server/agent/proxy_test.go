package agent

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// startTestProxy starts a ContainerProxy on a random port and returns it.
func startTestProxy(t *testing.T, extra []string) *ContainerProxy {
	t.Helper()
	p := NewContainerProxy("127.0.0.1:0", extra)
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
	p := NewContainerProxy("127.0.0.1:0", nil)
	for _, h := range defaultAllowedHosts {
		if !p.isAllowedConnect(h) {
			t.Errorf("expected %s to be allowed by default", h)
		}
	}
}

func TestNewContainerProxy_AnthropicAllowed(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", nil)
	if !p.isAllowedConnect("api.anthropic.com") {
		t.Error("api.anthropic.com must be allowed (containers connect directly with injected key)")
	}
	if !p.isAllowedConnect("api.anthropic.com:443") {
		t.Error("api.anthropic.com:443 must be allowed (port-stripped match)")
	}
}

func TestNewContainerProxy_ExtraAllowedHosts(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", []string{"example.com", ""})
	if !p.isAllowedConnect("example.com") {
		t.Error("expected example.com to be allowed (extra host)")
	}
	// Empty string must not panic
	if p.isAllowedConnect("") {
		t.Error("empty host must not be allowed")
	}
}

func TestNewContainerProxy_BlockedByDefault(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", nil)
	blocked := []string{"evil.com", "google.com", "attacker.io"}
	for _, h := range blocked {
		if p.isAllowedConnect(h) {
			t.Errorf("expected %s to be blocked by default", h)
		}
	}
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
	p := NewContainerProxy("127.0.0.1:0", []string{"example.com"})
	if !p.isAllowedConnect("example.com:443") {
		t.Error("should allow example.com:443 (strips port)")
	}
	if !p.isAllowedConnect("example.com:8080") {
		t.Error("should allow example.com:8080 (strips port)")
	}
}

func TestIsAllowedConnect_CaseInsensitive(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", []string{"Example.Com"})
	if !p.isAllowedConnect("example.com") {
		t.Error("host matching must be case-insensitive")
	}
	if !p.isAllowedConnect("EXAMPLE.COM") {
		t.Error("host matching must be case-insensitive (upper)")
	}
}

// ---------------------------------------------------------------------------
// Proxy server — start/stop
// ---------------------------------------------------------------------------

func TestProxy_Start(t *testing.T) {
	p := startTestProxy(t, nil)
	if p.Addr() == "" {
		t.Fatal("proxy Addr must not be empty after Start")
	}
}

func TestProxy_AddrUpdatedAfterBind(t *testing.T) {
	p := NewContainerProxy("127.0.0.1:0", nil)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Stop()
	if len(p.Addr()) == 0 || p.Addr()[len(p.Addr())-2:] == ":0" {
		t.Fatalf("Addr should not be :0 after Start, got %s", p.Addr())
	}
}

func TestProxy_StopIdempotent(t *testing.T) {
	p := startTestProxy(t, nil)
	p.Stop()
	p.Stop() // second call must not panic
}

// ---------------------------------------------------------------------------
// Egress whitelist — CONNECT tunnel enforcement
// ---------------------------------------------------------------------------

func TestEgress_BlockedCONNECT(t *testing.T) {
	p := startTestProxy(t, nil)
	client := proxyClient(t, p.Addr())

	// Attempt HTTPS CONNECT to a non-whitelisted host.
	resp, err := client.Get("https://google.com")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error (connection refused) for non-whitelisted CONNECT")
	}
}

func TestEgress_AnthropicCONNECTAllowed(t *testing.T) {
	// api.anthropic.com must be reachable via CONNECT: containers use the real
	// API key injected via `podman exec -e`, not via a proxy relay.
	p := NewContainerProxy("127.0.0.1:0", nil)
	if !p.isAllowedConnect("api.anthropic.com:443") {
		t.Error("api.anthropic.com:443 must be allowed in whitelist")
	}
}
