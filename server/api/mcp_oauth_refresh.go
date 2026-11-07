package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/esonic/nexbox/store"
)

const (
	mcpOAuthRefreshInterval       = 10 * time.Minute
	mcpOAuthRefreshWindow         = mcpOAuthRefreshInterval + 5*time.Minute
	mcpOAuthRequestRefreshWindow  = time.Minute
	mcpOAuthUnknownExpiryInterval = time.Hour
)

type oauthRefreshError struct {
	status int
	code   string
}

func (e *oauthRefreshError) Error() string {
	return fmt.Sprintf("OAuth refresh rejected (HTTP %d, %s)", e.status, e.code)
}
func (e *oauthRefreshError) requiresAuthorization() bool {
	if e.status != http.StatusBadRequest && e.status != http.StatusUnauthorized {
		return false
	}
	return e.code == "invalid_grant" || e.code == "invalid_client" || e.code == "unauthorized_client"
}

// managedOAuthHeader serializes refreshes and reloads credentials under the lock.
// rejected is the Authorization value that received a 401, or empty for normal use.
func (s *Server) managedOAuthHeader(mcp *store.InstalledMCP, rejected string) (string, error) {
	return s.managedOAuthHeaderForWindow(mcp, rejected, mcpOAuthRequestRefreshWindow)
}

func (s *Server) managedOAuthHeaderForWindow(mcp *store.InstalledMCP, rejected string, window time.Duration) (string, error) {
	configKey, err := mcpOAuthConfigKey(mcp)
	if err != nil {
		return "", err
	}
	unlock := s.lockMCPOAuth(configKey)
	defer unlock()
	connection, err := s.resolveMCPOAuthConnection(mcp, configKey)
	if err != nil {
		return "", err
	}
	if connection == nil {
		return "", nil
	}
	tok := &connection.Token
	if tok.AccessToken == "" {
		return "", nil
	}
	header := "Bearer " + tok.AccessToken
	if rejected != "" && header != rejected {
		// A different token may have been issued while the rejected request was
		// in flight. Apply normal expiry checks: it may already have expired too.
		rejected = ""
	}
	if rejected == "" {
		if tok.ExpiresAt.IsZero() {
			obtained := tok.ObtainedAt
			if obtained.IsZero() {
				obtained = tok.UpdatedAt
			}
			if tok.RefreshToken == "" || (!obtained.IsZero() && time.Since(obtained) < mcpOAuthUnknownExpiryInterval) {
				return header, nil
			}
		} else {
			// A freshly issued short-lived token must not be refreshed on every
			// request merely because its entire lifetime is below the window.
			if window <= mcpOAuthRequestRefreshWindow && !tok.ObtainedAt.IsZero() && tok.ExpiresAt.After(tok.ObtainedAt) {
				if fraction := tok.ExpiresAt.Sub(tok.ObtainedAt) / 5; fraction < window {
					window = fraction
				}
			}
			if time.Until(tok.ExpiresAt) > window {
				return header, nil
			}
		}
	}
	usable := tok.ExpiresAt.IsZero() || time.Now().Before(tok.ExpiresAt)
	if tok.RefreshToken == "" || tok.TokenEndpoint == "" {
		if usable && rejected == "" {
			return header, nil
		}
		return "", nil
	}
	refreshed, err := refreshOAuthToken(tok)
	if err != nil {
		if usable && rejected == "" {
			if window == mcpOAuthRefreshWindow {
				log.Printf("[mcp-oauth] %s proactive refresh failed; keeping usable access token: %v", mcp.Name, err)
			}
			return header, nil
		}
		if failure, ok := err.(*oauthRefreshError); ok && failure.requiresAuthorization() {
			return "", nil
		}
		return "", fmt.Errorf("temporary MCP token refresh failure: %w", err)
	}
	refreshed.MCPName, refreshed.ProjectID = tok.MCPName, tok.ProjectID
	connection.Token = *refreshed
	if err := s.Store.SaveMCPOAuthConnection(*connection); err != nil {
		return "", fmt.Errorf("persist refreshed MCP token: %w", err)
	}
	return "Bearer " + refreshed.AccessToken, nil
}

// StartMCPOAuthRefresher runs immediately at startup and every ten minutes thereafter.
// It includes global and project installations, even when no conversation is active.
func (s *Server) StartMCPOAuthRefresher(ctx context.Context) {
	ticker := time.NewTicker(mcpOAuthRefreshInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.refreshInstalledMCPOAuth(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) refreshInstalledMCPOAuth(ctx context.Context) {
	if s.Store == nil {
		return
	}
	// Also recover cleanup interrupted by a previous shutdown between deleting
	// the final installation and removing its authorization.
	unlockInstallations := s.lockMCPOAuth("installations")
	if err := s.cleanupUnusedMCPOAuthConnections(); err != nil {
		unlockInstallations()
		log.Printf("[mcp-oauth] clean up unused authorizations failed: %v", err)
		return
	}
	installed, err := s.Store.ListInstalledMCPs("*")
	unlockInstallations()
	if err != nil {
		log.Printf("[mcp-oauth] list installations failed: %v", err)
		return
	}
	seen := make(map[string]bool)
	for _, mcp := range installed {
		if ctx.Err() != nil {
			return
		}
		if mcp.AuthType != "oauth" && mcp.AuthType != "oauth_discovery" && mcp.AuthType != "none" {
			continue
		}
		key, err := mcpOAuthConfigKey(&mcp)
		if err != nil {
			continue
		}
		unlock := s.lockMCPOAuth(key)
		connection, err := s.resolveMCPOAuthConnection(&mcp, key)
		unlock()
		if err != nil {
			log.Printf("[mcp-oauth] %s: connection lookup failed", mcp.Name)
			continue
		}
		if connection == nil || seen[connection.ID] {
			continue
		}
		seen[connection.ID] = true
		if _, err := s.managedOAuthHeaderForWindow(&mcp, "", mcpOAuthRefreshWindow); err != nil {
			log.Printf("[mcp-oauth] %s: %v", mcp.Name, err)
		}
	}
}

type mcpOAuthTransport struct {
	server *Server
	mcp    *store.InstalledMCP
}

func (t *mcpOAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || (t.mcp.AuthType != "oauth" && t.mcp.AuthType != "oauth_discovery") {
		return resp, err
	}
	if req.Body != nil && req.GetBody == nil {
		return resp, nil
	}
	header, refreshErr := t.server.managedOAuthHeader(t.mcp, req.Header.Get("Authorization"))
	if refreshErr != nil {
		resp.Body.Close()
		return nil, refreshErr
	}
	if header == "" {
		return resp, nil
	}
	retry := req.Clone(req.Context())
	if req.Body != nil {
		retry.Body, err = req.GetBody()
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
	}
	resp.Body.Close()
	retry.Header.Set("Authorization", header)
	return http.DefaultTransport.RoundTrip(retry)
}
