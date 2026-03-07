package main

import (
//	"crypto/tls"
//	"crypto/x509"
	"encoding/base64"
	"flag"
//	"fmt"
	"log"
	"net/http"
//	"net/url"
	"os"
	"strings"

//	"github.com/armon/go-socks5"
	"github.com/elazarl/goproxy"
)

var (
//	onlineURL = flag.String("online-url", "", "")
	verbose   = flag.Bool("verbose", false, "")
//	exportCA  = flag.Bool("export-ca", false, "Export CA certificate to ca.crt file")
//	token     = flag.String("token", "", "Token for the proxy")
//	dele      = flag.Bool("dele", false, "delegate the proxy")

	// SSH server flags
//	sshPassword  = flag.String("ssh-password", "`123", "")
//	sshVSCodeDir = flag.String("ssh-ide-dir", "/Users/ericdu/meili/eric.du_dacs_at_okg.com/114/.cursor-server", "")
//	sshHomeDir   = flag.String("ssh-home", "/Users/ericdu/meili/eric.du_dacs_at_okg.com/114/home", "")
)

func main() {
	flag.Parse()

//	if *exportCA {
//		exportCACertificate()
//		return
//	}

//	if *dele {
//		// sock and pro proxy
//		delegate(*onlineURL)
//	} else {
		commonProxy()
//	}
}

//func delegate(urlPath string) {
//	targetURL, err := url.Parse(urlPath)
//	if err != nil {
//		log.Fatalf("invalid forward URL: %v", err)
//	}
//
//	proxy := goproxy.NewProxyHttpServer()
//	proxy.Verbose = *verbose
//
//	setCA(proxy)
//
//	proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)
//
//	proxy.OnRequest().DoFunc(
//		func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
//			return handleRequest(req, ctx, targetURL)
//		})
//
//	if *verbose {
//		fmt.Printf("Verbose logging enabled\n")
//	}
//
//	log.Fatal(http.ListenAndServe("127.0.0.1:8084", proxy))
//}

func commonProxy() {
	auth := os.Getenv("xuser")

	// sock5 for SSH etc.
//	conf := &socks5.Config{}
//	if auth != "" {
//		parts := strings.SplitN(auth, ":", 2)
//		if len(parts) == 2 {
//			conf.Credentials = socks5.StaticCredentials{
//				parts[0]: parts[1],
//			}
//		}
//	}
//	server, err := socks5.New(conf)
//	if err != nil {
//		panic(err)
//	}

//	go func() {
//		if err := server.ListenAndServe("tcp", "0.0.0.0:8083"); err != nil {
//			panic(err)
//		}
//	}()

	// SSH server for VSCode/Cursor Remote Development
	//StartSSHServerAsync(8082, *sshPassword, *sshVSCodeDir, *sshHomeDir)

	// normal
	proxy := goproxy.NewProxyHttpServer()
	if *verbose {
		proxy.Verbose = true
	}

	var handler http.Handler = proxy
	if auth != "" {
		handler = proxyAuthHandler(proxy, auth)
	}

	log.Fatal(http.ListenAndServe("0.0.0.0:8081", handler))
}

func proxyAuthHandler(proxy *goproxy.ProxyHttpServer, auth string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyAuthHeader := r.Header.Get("Proxy-Authorization")
		if proxyAuthHeader == "" {
			// Return 407 so browser prompts for credentials
			w.Header().Set("Proxy-Authenticate", `Basic realm="Proxy"`)
			http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
			return
		}

		// Parse Basic auth: "Basic base64(user:pass)"
		parts := strings.SplitN(proxyAuthHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Basic" {
			// Wrong format, return 500 to hide
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		decoded, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil || string(decoded) != auth {
			// Wrong password, return 500 to hide
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		proxy.ServeHTTP(w, r)
	})
}

//func exportCACertificate() {
//	err := os.WriteFile("ca.crt", goproxy.CA_CERT, 0644)
//	if err != nil {
//		log.Fatalf("Failed to write CA certificate: %v", err)
//	}
//	fmt.Printf("CA certificate exported to ca.crt\n")
//	fmt.Printf("\nTo trust this certificate:\n")
//	fmt.Printf("macOS: sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ca.crt\n")
//	fmt.Printf("Linux: sudo cp ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates\n")
//	fmt.Printf("Windows: certlm.msc -> Trusted Root Certification Authorities -> Import ca.crt\n")
//	fmt.Printf("\nAfter importing, restart your browser.\n")
//}

//func handleRequest(req *http.Request, ctx *goproxy.ProxyCtx, forwardURL *url.URL) (*http.Request, *http.Response) {
//	originalURL := req.URL.String()
//	if !req.URL.IsAbs() {
//		scheme := "http"
//		if req.TLS != nil || ctx.Req.URL.Scheme == "https" {
//			scheme = "https"
//		}
//		originalURL = fmt.Sprintf("%s://%s%s", scheme, req.Host, req.URL.RequestURI())
//	}
//
//	if *verbose {
//		log.Printf("Intercepted request: %s %s", req.Method, originalURL)
//	}
//
//	req.URL = forwardURL
//	req.Host = forwardURL.Host
//	req.Header.Set("X-Target-Url", originalURL)
//	req.Header.Set("X-TK", *token)
//
//	return req, nil
//}

//func setCA(proxy *goproxy.ProxyHttpServer) {
//	var err error
//	goproxy.GoproxyCa, err = tls.X509KeyPair(goproxy.CA_CERT, goproxy.CA_KEY)
//	if err != nil {
//		panic("Could not load builtin CA certificate: " + err.Error())
//	}
//
//	cert, err := x509.ParseCertificate(goproxy.GoproxyCa.Certificate[0])
//	if err != nil {
//		panic("Could not parse builtin CA certificate: " + err.Error())
//	}
//	goproxy.GoproxyCa.Leaf = cert
//}
