package agent

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/elazarl/goproxy"
)

// defaultAllowedHosts lists the hostnames containers may reach via HTTPS CONNECT.
//
// Additional per-connector domains can be appended via proxy_allowed_hosts in
// config.json. See docs/ops_manual.md §10 for a per-connector domain table.
var defaultAllowedHosts = []string{
	// Anthropic API — Claude CLI needs this for all API calls
	"api.anthropic.com",

	// Claude CLI internals
	"statsig.anthropic.com",              // feature-flag telemetry
	"sentry.io",                          // Claude CLI error reporting
	"o4507971705behance.ingest.sentry.io", // Sentry ingest

	// Package managers — needed by npm-based MCP connectors (npx …)
	"registry.npmjs.org", // npm package registry
	"npmjs.org",          // npm redirect target

	// Git hosting — needed for git clone/push and GitHub/GitLab MCP connectors
	"github.com",
	"api.github.com",
	"gitlab.com",

	// Marketplace MCP connectors (HTTP-based, enabled when connector is installed)
	"mcp.slack.com",         // Slack MCP
	"mcp.stripe.com",        // Stripe MCP
	"mcp.linear.app",        // Linear MCP
	"mcp.asana.com",         // Asana MCP
	"api.githubcopilot.com", // GitHub Copilot MCP
	"api.greptile.com",      // Greptile MCP
	"mcp.supabase.com",      // Supabase MCP
}

// ContainerProxy is an HTTP egress proxy embedded inside the BoxNex server.
// It enforces a domain whitelist on all outbound HTTPS CONNECT tunnels from
// containers. Plain HTTP requests to non-whitelisted hosts are also blocked.
//
// The real API key is never stored here — it is injected directly into the
// claude-runner process via `podman exec -e` at execution time (see ExecClaudeInContainer).
type ContainerProxy struct {
	mu            sync.RWMutex
	allowedHosts  map[string]bool // lowercase hostname → true
	proxyAddr     string          // listen address: "<ip>:<port>"
	containerAddr string          // address containers should use to reach the proxy
	server        *http.Server
}

// NewContainerProxy creates a proxy that enforces the given domain whitelist
// on CONNECT tunnels. extraAllowedHosts are merged with defaultAllowedHosts.
func NewContainerProxy(listenAddr string, extraAllowedHosts []string) *ContainerProxy {
	p := &ContainerProxy{
		allowedHosts: make(map[string]bool),
		proxyAddr:    listenAddr,
	}
	for _, h := range defaultAllowedHosts {
		p.allowedHosts[strings.ToLower(h)] = true
	}
	for _, h := range extraAllowedHosts {
		if h != "" {
			p.allowedHosts[strings.ToLower(h)] = true
		}
	}
	return p
}

// Addr returns the address the proxy is (or will be) listening on.
func (p *ContainerProxy) Addr() string {
	return p.proxyAddr
}

// SetContainerAddr overrides the address injected into container HTTP_PROXY env
// vars. Use this when the proxy is bound to 0.0.0.0 but containers must reach
// it via a specific host IP (e.g. with Podman rootless + pasta networking where
// the bridge gateway IP is virtual and unreachable from containers).
func (p *ContainerProxy) SetContainerAddr(addr string) {
	p.containerAddr = addr
}

// ContainerAddr returns the address containers should use in HTTP_PROXY env vars.
// Falls back to Addr() if no explicit container address has been set.
func (p *ContainerProxy) ContainerAddr() string {
	if p.containerAddr != "" {
		return p.containerAddr
	}
	return p.proxyAddr
}

// isAllowedConnect reports whether a CONNECT tunnel to host is permitted.
// host may include a port suffix (e.g. "example.com:443").
func (p *ContainerProxy) isAllowedConnect(host string) bool {
	h := host
	if !strings.HasPrefix(h, "[") {
		if idx := strings.LastIndex(h, ":"); idx >= 0 {
			h = h[:idx]
		}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.allowedHosts[strings.ToLower(h)]
}

// Start binds the proxy listener and begins serving in the background.
// It returns an error if the TCP bind fails; all subsequent errors are logged.
func (p *ContainerProxy) Start() error {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false

	// ── Plain HTTP whitelist ──────────────────────────────────────────────────
	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		host := req.URL.Hostname()
		if !p.isAllowedConnect(req.URL.Host) && !p.isAllowedConnect(host) {
			log.Printf("[proxy] blocked HTTP %s %s", req.Method, req.URL.Host)
			return req, goproxy.NewResponse(req, "text/plain", http.StatusForbidden,
				"Access denied by BoxNex egress policy")
		}
		return req, nil
	})

	// ── CONNECT whitelist ─────────────────────────────────────────────────────
	proxy.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(
		func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			if !p.isAllowedConnect(host) {
				log.Printf("[proxy] blocked CONNECT to %s", host)
				return goproxy.RejectConnect, host
			}
			log.Printf("[proxy] allowed CONNECT to %s", host)
			return goproxy.OkConnect, host
		},
	))

	ln, err := net.Listen("tcp", p.proxyAddr)
	if err != nil {
		return fmt.Errorf("proxy listen %s: %w", p.proxyAddr, err)
	}
	// Update proxyAddr to the actual bound address (useful when port was 0).
	p.proxyAddr = ln.Addr().String()

	p.server = &http.Server{Handler: proxy}
	go func() {
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[proxy] server stopped: %v", err)
		}
	}()
	log.Printf("[proxy] egress proxy listening on %s", p.proxyAddr)
	return nil
}

// Stop gracefully shuts down the proxy server, waiting up to 5 s for
// in-flight requests to complete before forcefully closing connections.
func (p *ContainerProxy) Stop() {
	if p.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.server.Shutdown(ctx); err != nil {
		log.Printf("[proxy] shutdown: %v", err)
	}
}
