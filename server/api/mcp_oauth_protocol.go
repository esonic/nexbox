package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/esonic/nexbox/store"
)

type oauthDiscoveryDoc struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	GrantTypes            []string `json:"grant_types_supported"`
	AuthMethods           []string `json:"token_endpoint_auth_methods_supported"`
	Scopes                []string `json:"scopes_supported"`
	Resource              string   `json:"-"`
	Scope                 string   `json:"-"`
}

func validateMCPOAuthURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("OAuth endpoint must be an absolute HTTP URL without userinfo or fragment")
	}
	return nil
}

func getMCPOAuthJSON(ctx context.Context, raw string, out any) error {
	if err := validateMCPOAuthURL(raw); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oauthDiscoveryHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("OAuth metadata request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OAuth metadata returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("invalid OAuth metadata")
	}
	return nil
}

func discoverOAuthIssuer(ctx context.Context, issuer string) (*oauthDiscoveryDoc, error) {
	u, err := url.Parse(issuer)
	if err != nil || validateMCPOAuthURL(issuer) != nil {
		return nil, fmt.Errorf("invalid OAuth issuer")
	}
	path := strings.TrimRight(u.Path, "/")
	paths := []string{"/.well-known/oauth-authorization-server" + path, path + "/.well-known/openid-configuration"}
	// Also tolerate issuers exposing OIDC metadata using the RFC 8414 path
	// convention, while trying OIDC's issuer-relative location first.
	if path != "" {
		paths = append(paths, "/.well-known/openid-configuration"+path)
	}
	for _, metadataPath := range paths {
		candidate := *u
		candidate.Path = metadataPath
		candidate.RawPath = ""
		candidate.RawQuery = ""
		var doc oauthDiscoveryDoc
		if err := getMCPOAuthJSON(ctx, candidate.String(), &doc); err != nil {
			continue
		}
		// Reject a tenant/issuer routing mismatch before trusting any endpoint.
		// Older MCP authorization metadata may omit issuer; retain compatibility
		// with those documents, but an explicitly declared issuer must match.
		if doc.Issuer != "" && doc.Issuer != issuer {
			continue
		}
		if validateMCPOAuthURL(doc.AuthorizationEndpoint) != nil || validateMCPOAuthURL(doc.TokenEndpoint) != nil {
			continue
		}
		if doc.RegistrationEndpoint != "" && validateMCPOAuthURL(doc.RegistrationEndpoint) != nil {
			continue
		}
		return &doc, nil
	}
	return nil, fmt.Errorf("OAuth authorization server metadata unavailable")
}

var mcpOAuthChallengeParam = regexp.MustCompile(`(?i)(?:^|[,\s])(resource_metadata|scope)\s*=\s*(?:"([^"]*)"|([^,\s]+))`)

// Discover the protected resource before the authorization server. In particular,
// scope/offline_access must be requested during consent, before any refresher can
// work; scheduling refresh cannot recover a refresh token never issued upstream.
func discoverInstalledMCPOAuth(mcp *store.InstalledMCP) (*oauthDiscoveryDoc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	entry, err := parseInstalledMCPManifest(mcp.Manifest)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(entry.URL)
	if err != nil || validateMCPOAuthURL(entry.URL) != nil {
		return nil, fmt.Errorf("invalid MCP resource URL")
	}
	var values map[string]string
	if mcp.EnvValues != "" {
		if err := json.Unmarshal([]byte(mcp.EnvValues), &values); err != nil {
			return nil, err
		}
	}
	headers, _, err := resolveMCPHeaderTemplates(entry.Headers, values)
	if err != nil {
		return nil, err
	}
	challengeURL, challengeScope := "", ""
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"BeeHub","version":"1"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.URL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if resp, err := oauthDiscoveryHTTPClient.Do(req); err == nil {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			for _, header := range resp.Header.Values("WWW-Authenticate") {
				if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(header)), "bearer ") {
					continue
				}
				for _, match := range mcpOAuthChallengeParam.FindAllStringSubmatch(header, -1) {
					value := match[2]
					if value == "" {
						value = match[3]
					}
					switch strings.ToLower(match[1]) {
					case "resource_metadata":
						challengeURL = value
					case "scope":
						challengeScope = value
					}
				}
			}
		}
		resp.Body.Close()
	}
	type resourceMetadata struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
		Scopes   []string `json:"scopes_supported"`
	}
	type candidate struct{ URL, Resource string }
	var candidates []candidate
	if challengeURL != "" {
		candidates = append(candidates, candidate{challengeURL, entry.URL})
	}
	resourcePath := strings.TrimRight(u.Path, "/")
	for depth := 0; depth < 10; depth++ {
		resource := *u
		resource.Path = resourcePath
		resource.RawPath = ""
		resource.RawQuery = ""
		resource.Fragment = ""
		metadata := resource
		metadata.Path = "/.well-known/oauth-protected-resource" + resourcePath
		candidates = append(candidates, candidate{metadata.String(), resource.String()})
		metadata.Path = "/.well-known/oauth-protected-resource"
		candidates = append(candidates, candidate{metadata.String(), resource.String()})
		if resourcePath == "" {
			break
		}
		if i := strings.LastIndex(resourcePath, "/"); i >= 0 {
			resourcePath = resourcePath[:i]
		} else {
			resourcePath = ""
		}
	}
	seen := make(map[string]bool)
	cache := make(map[string]*resourceMetadata)
	for _, candidate := range candidates {
		key := candidate.URL + "\x00" + candidate.Resource
		if seen[key] {
			continue
		}
		seen[key] = true
		resource, loaded := cache[candidate.URL]
		if !loaded {
			var metadata resourceMetadata
			if err := getMCPOAuthJSON(ctx, candidate.URL, &metadata); err == nil {
				resource = &metadata
			}
			cache[candidate.URL] = resource
		}
		if resource == nil || resource.Resource != candidate.Resource || len(resource.Servers) == 0 {
			continue
		}
		doc, err := discoverOAuthIssuer(ctx, resource.Servers[0])
		if err != nil {
			return nil, err
		}
		doc.Resource = resource.Resource
		doc.Scope = challengeScope
		if doc.Scope == "" {
			doc.Scope = strings.Join(resource.Scopes, " ")
		}
		if slices.Contains(doc.Scopes, "offline_access") || slices.Contains(resource.Scopes, "offline_access") {
			if !slices.Contains(strings.Fields(doc.Scope), "offline_access") {
				doc.Scope = strings.TrimSpace(doc.Scope + " offline_access")
			}
		}
		return doc, nil
	}
	origin := *u
	origin.Path = ""
	origin.RawPath = ""
	origin.RawQuery = ""
	origin.Fragment = ""
	doc, err := discoverOAuthIssuer(ctx, origin.String())
	if err != nil {
		return nil, err
	}
	doc.Scope = challengeScope
	if slices.Contains(doc.Scopes, "offline_access") && !slices.Contains(strings.Fields(doc.Scope), "offline_access") {
		doc.Scope = strings.TrimSpace(doc.Scope + " offline_access")
	}
	return doc, nil
}

func chooseMCPOAuthAuthMethod(methods []string, secret, preferred string) (string, error) {
	supported := func(method string) bool { return len(methods) == 0 || slices.Contains(methods, method) }
	if preferred != "" && supported(preferred) && (preferred == "none" || secret != "") {
		return preferred, nil
	}
	if secret != "" {
		for _, method := range []string{"client_secret_basic", "client_secret_post"} {
			if supported(method) {
				return method, nil
			}
		}
	}
	if supported("none") {
		return "none", nil
	}
	return "", fmt.Errorf("OAuth server requires a supported client authentication method and client secret")
}

func registerMCPOAuthClient(doc *oauthDiscoveryDoc, redirectURI, scope string) (*store.MCPOAuthToken, error) {
	method := "none"
	if len(doc.AuthMethods) > 0 && !slices.Contains(doc.AuthMethods, "none") {
		method = "client_secret_basic"
		if !slices.Contains(doc.AuthMethods, method) {
			method = "client_secret_post"
		}
		if !slices.Contains(doc.AuthMethods, method) {
			return nil, fmt.Errorf("unsupported OAuth client authentication methods")
		}
	}
	grants := []string{"authorization_code"}
	if len(doc.GrantTypes) == 0 || slices.Contains(doc.GrantTypes, "refresh_token") {
		grants = append(grants, "refresh_token")
	}
	metadata := map[string]any{"client_name": "BeeHub MCP", "redirect_uris": []string{redirectURI}, "token_endpoint_auth_method": method, "grant_types": grants, "response_types": []string{"code"}}
	if scope != "" {
		metadata["scope"] = scope
	}
	body, _ := json.Marshal(metadata)
	resp, err := oauthDiscoveryHTTPClient.Post(doc.RegistrationEndpoint, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("OAuth client registration request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OAuth client registration returned HTTP %d", resp.StatusCode)
	}
	var client struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		AuthMethod   string `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&client); err != nil || client.ClientID == "" {
		return nil, fmt.Errorf("invalid OAuth client registration response")
	}
	if client.AuthMethod != "" {
		method = client.AuthMethod
	}
	if (method == "client_secret_basic" || method == "client_secret_post") && client.ClientSecret == "" {
		return nil, fmt.Errorf("OAuth registration did not provide the required client secret")
	}
	method, err = chooseMCPOAuthAuthMethod(doc.AuthMethods, client.ClientSecret, method)
	if err != nil {
		return nil, err
	}
	return &store.MCPOAuthToken{ClientID: client.ClientID, ClientSecret: client.ClientSecret, AuthMethod: method, RegisteredGrantTypes: grants}, nil
}

func sameMCPOAuthScope(a, b string) bool {
	left, right := strings.Fields(a), strings.Fields(b)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(slices.Compact(left), slices.Compact(right))
}

type tokenResponse struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	ExpiresIn    json.RawMessage `json:"expires_in"`
	TokenType    string          `json:"token_type"`
	Scope        string          `json:"scope"`
}

func exchangeMCPOAuthToken(client *store.MCPOAuthToken, form url.Values) (*store.MCPOAuthToken, error) {
	if client.Resource != "" {
		form.Set("resource", client.Resource)
	}
	method := client.AuthMethod
	if method == "" {
		method = "none"
	}
	switch method {
	case "none":
		form.Set("client_id", client.ClientID)
	case "client_secret_post":
		form.Set("client_id", client.ClientID)
		form.Set("client_secret", client.ClientSecret)
	case "client_secret_basic":
	default:
		return nil, fmt.Errorf("unsupported OAuth client authentication method")
	}
	req, err := http.NewRequest(http.MethodPost, client.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("invalid OAuth token endpoint")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if method == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(client.ClientID), url.QueryEscape(client.ClientSecret))
	}
	resp, err := oauthDiscoveryHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OAuth token request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("OAuth token response unreadable")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &failure)
		return nil, &oauthRefreshError{status: resp.StatusCode, code: failure.Error}
	}
	var response tokenResponse
	if err := json.Unmarshal(body, &response); err != nil || response.AccessToken == "" {
		return nil, fmt.Errorf("invalid OAuth token response")
	}
	var seconds int64
	if len(response.ExpiresIn) > 0 && string(response.ExpiresIn) != "null" {
		raw := strings.TrimSpace(string(response.ExpiresIn))
		if strings.HasPrefix(raw, `"`) {
			if err := json.Unmarshal(response.ExpiresIn, &raw); err != nil {
				return nil, fmt.Errorf("invalid OAuth token lifetime")
			}
		}
		seconds, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || seconds < 0 || seconds > int64((1<<63-1)/int64(time.Second)) {
			return nil, fmt.Errorf("invalid OAuth token lifetime")
		}
	}
	out := *client
	out.AccessToken, out.RefreshToken = response.AccessToken, response.RefreshToken
	out.ObtainedAt, out.UpdatedAt = time.Now(), time.Now()
	out.ExpiresAt = time.Time{}
	if seconds > 0 {
		out.ExpiresAt = out.ObtainedAt.Add(time.Duration(seconds) * time.Second)
	}
	if response.Scope != "" {
		out.Scope = response.Scope
	}
	if response.TokenType != "" {
		out.TokenType = response.TokenType
	}
	if out.TokenType == "" {
		out.TokenType = "Bearer"
	}
	if !strings.EqualFold(out.TokenType, "Bearer") {
		return nil, fmt.Errorf("unsupported OAuth token type")
	}
	return &out, nil
}

func refreshOAuthToken(tok *store.MCPOAuthToken) (*store.MCPOAuthToken, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken}}
	if tok.Scope != "" {
		form.Set("scope", tok.Scope)
	}
	out, err := exchangeMCPOAuthToken(tok, form)
	if err != nil {
		return nil, err
	}
	if out.RefreshToken == "" {
		out.RefreshToken = tok.RefreshToken
	}
	return out, nil
}
