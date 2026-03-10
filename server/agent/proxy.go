package agent

import (
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
// api.anthropic.com is intentionally absent: it is handled by the API-key relay
// path (plain HTTP via ANTHROPIC_BASE_URL), not via CONNECT tunnels.
//
// Additional per-connector domains can be appended via proxy_allowed_hosts in
// config.json. See docs/ops_manual.md §10 for a per-connector domain table.
var defaultAllowedHosts = []string{
	// Claude CLI internals
	"statsig.anthropic.com",             // feature-flag telemetry
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
	"mcp.slack.com",           // Slack MCP
	"mcp.stripe.com",          // Stripe MCP
	"mcp.linear.app",          // Linear MCP
	"mcp.asana.com",           // Asana MCP
	"api.githubcopilot.com",   // GitHub Copilot MCP
	"api.greptile.com",        // Greptile MCP
	"mcp.supabase.com",        // Supabase MCP
}

// sessionEntry records a registered container session.
type sessionEntry struct {
	projectID string
	createdAt time.Time
}

// ContainerProxy is an HTTP egress proxy embedded inside the BoxNex server.
//
// Two roles in one listener (port shared to reduce firewall rules):
//
//  1. API-key relay: when Claude CLI sends requests with ANTHROPIC_BASE_URL
//     pointing at the proxy, the proxy validates the per-container session token
//     (carried in the Authorization header) and forwards to api.anthropic.com
//     with the real API key. The real key never enters the container environment.
//
//  2. Egress whitelist: HTTPS CONNECT tunnels are only allowed to
//     the domains in allowedHosts. All other outbound connections are rejected.
type ContainerProxy struct {
	mu           sync.RWMutex
	sessions     map[string]sessionEntry // sessionToken → entry
	allowedHosts map[string]bool         // lowercase hostname → true
	realAuthToken   string                  // CLAUDE_CODE_OAUTH_TOKEN, stored only in server memory, never sent to containers
	proxyAddr    string                  // listen address: "<ip>:<port>"
	server       *http.Server
}

// NewContainerProxy creates a proxy that relays Anthropic API calls using
// realAuthToken and enforces the given domain whitelist on CONNECT tunnels.
// extraAllowedHosts are merged with defaultAllowedHosts.
func NewContainerProxy(listenAddr, realAuthToken string, extraAllowedHosts []string) *ContainerProxy {
	p := &ContainerProxy{
		sessions:     make(map[string]sessionEntry),
		allowedHosts: make(map[string]bool),
		realAuthToken:   realAuthToken,
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

// RegisterSession records a session token associated with projectID.
// The token must be used as the ANTHROPIC_API_KEY inside the container.
func (p *ContainerProxy) RegisterSession(token, projectID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions[token] = sessionEntry{projectID: projectID, createdAt: time.Now()}
}

// RevokeSession removes a session token immediately, invalidating any further
// API calls from the container that held it.
func (p *ContainerProxy) RevokeSession(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, token)
}

// SessionCount returns the number of currently active session tokens.
func (p *ContainerProxy) SessionCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.sessions)
}

// Addr returns the address the proxy is (or will be) listening on.
func (p *ContainerProxy) Addr() string {
	return p.proxyAddr
}

// lookupSession returns (projectID, true) when token is a valid registered session.
func (p *ContainerProxy) lookupSession(token string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.sessions[token]
	if !ok {
		return "", false
	}
	return e.projectID, true
}

// isAllowedConnect reports whether a CONNECT tunnel to host is permitted.
// host may include a port suffix (e.g. "example.com:443").
func (p *ContainerProxy) isAllowedConnect(host string) bool {
	h := host
	// Strip port: find last colon that is not inside an IPv6 bracket.
	if !strings.HasPrefix(h, "[") {
		if idx := strings.LastIndex(h, ":"); idx >= 0 {
			h = h[:idx]
		}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.allowedHosts[strings.ToLower(h)]
}

// isAnthropicRelay detects requests that Claude CLI sends when ANTHROPIC_BASE_URL
// points at the proxy. Detection heuristic: the request host matches our listen
// address (or its loopback/wildcard variants) AND the path begins with /v1/.
func (p *ContainerProxy) isAnthropicRelay(req *http.Request) bool {
	if !strings.HasPrefix(req.URL.Path, "/v1/") {
		return false
	}
	reqHost := req.URL.Host
	if reqHost == "" {
		reqHost = req.Host
	}
	_, proxyPort, err := net.SplitHostPort(p.proxyAddr)
	if err != nil {
		return false
	}
	_, reqPort, err := net.SplitHostPort(reqHost)
	if err != nil {
		return false
	}
	return reqPort == proxyPort
}

// Start binds the proxy listener and begins serving in the background.
// It returns an error if the TCP bind fails; all subsequent errors are logged.
func (p *ContainerProxy) Start() error {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false

	// ── Relay handler ────────────────────────────────────────────────────────
	// Intercepts requests where ANTHROPIC_BASE_URL == http://<proxyAddr>.
	// Validates the per-container session token, replaces it with the real
	// API key, and rewrites the destination to api.anthropic.com.
	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		if p.isAnthropicRelay(req) {
			return p.handleRelay(req)
		}

		// Plain HTTP requests to other hosts: enforce whitelist.
		host := req.URL.Hostname()
		if !p.isAllowedConnect(req.URL.Host) && !p.isAllowedConnect(host) {
			log.Printf("[proxy] blocked HTTP %s %s", req.Method, req.URL.Host)
			return req, goproxy.NewResponse(req, "text/plain", http.StatusForbidden,
				"Access denied by BoxNex egress policy")
		}
		return req, nil
	})

	// ── CONNECT handler ──────────────────────────────────────────────────────
	// Enforces the domain whitelist for HTTPS tunnels. CONNECT to
	// api.anthropic.com is blocked — containers must use the relay path instead.
	proxy.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(
		func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
			// Block api.anthropic.com via CONNECT; the relay handles those calls.
			h := host
			if idx := strings.LastIndex(h, ":"); idx >= 0 {
				h = h[:idx]
			}
			if strings.HasSuffix(strings.ToLower(h), "anthropic.com") {
				log.Printf("[proxy] blocked CONNECT to %s (use relay via ANTHROPIC_BASE_URL)", host)
				return goproxy.RejectConnect, host
			}
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

// Stop gracefully shuts down the proxy server.
func (p *ContainerProxy) Stop() {
	if p.server != nil {
		_ = p.server.Close()
	}
}

// handleRelay validates the session token in req, rewrites req to forward to
// api.anthropic.com using the real API key, and returns the modified request.
func (p *ContainerProxy) handleRelay(req *http.Request) (*http.Request, *http.Response) {
	authHeader := req.Header.Get("Authorization")
	fakeToken := strings.TrimPrefix(authHeader, "Bearer ")
	if fakeToken == authHeader { // no "Bearer " prefix
		fakeToken = ""
	}

	projectID, valid := p.lookupSession(fakeToken)
	if !valid {
		log.Printf("[proxy] relay rejected: unknown session token")
		return req, goproxy.NewResponse(req, "application/json", http.StatusUnauthorized,
			`{"type":"error","error":{"type":"authentication_error","message":"invalid session token"}}`)
	}

	log.Printf("[proxy] relay → api.anthropic.com%s (project=%s)", req.URL.Path, projectID)

	// Rewrite the request to target the real Anthropic API endpoint.
	req.URL.Scheme = "https"
	req.URL.Host = "api.anthropic.com"
	req.Host = "api.anthropic.com"
	req.Header.Set("Authorization", "Bearer "+p.realAuthToken)
	// Clear RequestURI so http.Transport recomputes it from req.URL.
	req.RequestURI = ""

	return req, nil
}
