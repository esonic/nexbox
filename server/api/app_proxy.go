package api

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

// ---------------------------------------------------------------------------
// App Reverse Proxy
// ---------------------------------------------------------------------------
//
// Forwards external traffic under a path prefix to a locally-running app
// (e.g. a Node.js server on 127.0.0.1:3001) so that a side app can be served
// through the same public entrypoint as nexbox without opening another port.
//
// Route:   /portfolio        -> http://127.0.0.1:3001/
//          /portfolio/{any}  -> http://127.0.0.1:3001/{any}
//
// The prefix is stripped before forwarding, so the upstream app sees normal
// root-relative paths. The upstream's built assets must therefore use a
// matching base path ("/portfolio/") so the browser requests come back here.
//
// Both the prefix and target are overridable via env vars to avoid recompiling
// when the upstream port changes:
//   NEX_APP_PROXY_PREFIX (default "/portfolio")
//   NEX_APP_PROXY_TARGET (default "http://127.0.0.1:3001")
//
// This route is intentionally outside the /api group and RequireAuth: the
// proxied app handles its own authentication.

func appProxyPrefix() string {
	if v := strings.TrimSpace(os.Getenv("NEX_APP_PROXY_PREFIX")); v != "" {
		return "/" + strings.Trim(v, "/")
	}
	return "/portfolio"
}

func appProxyTarget() string {
	if v := strings.TrimSpace(os.Getenv("NEX_APP_PROXY_TARGET")); v != "" {
		return v
	}
	return "http://127.0.0.1:3001"
}

// HandleAppProxy reverse-proxies a path-prefixed request to the local app.
func (s *Server) HandleAppProxy(w http.ResponseWriter, r *http.Request) {
	target, err := url.Parse(appProxyTarget())
	if err != nil {
		http.Error(w, "app proxy misconfigured", http.StatusInternalServerError)
		return
	}

	prefix := appProxyPrefix()

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host

			// Strip the public prefix so the upstream sees root-relative paths.
			p := strings.TrimPrefix(req.URL.Path, prefix)
			if p == "" {
				p = "/"
			}
			if !strings.HasPrefix(p, "/") {
				p = "/" + p
			}
			req.URL.Path = p
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			log.Printf("[app-proxy] upstream error for %s: %v", req.URL.Path, err)
			http.Error(rw, "app backend unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}
