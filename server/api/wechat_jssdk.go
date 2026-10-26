package api

import (
	crand "crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WeChat JS-SDK custom-share support.
//
// To make a shared page render as a rich card *inside WeChat* (the in-app
// browser "··· → 分享给朋友" flow), the page must call wx.config with a
// server-signed payload, then wx.updateAppMessageShareData. The signature is
// derived from a jsapi_ticket that only the backend can fetch (it needs the
// Official Account's appSecret). This file owns the token/ticket lifecycle and
// the signing endpoint.
//
// Credentials come from the environment (WECHAT_APPID / WECHAT_APPSECRET) and
// are never hardcoded or logged. WeChat's token/ticket APIs require the secret
// as a query parameter (there is no header-based auth), so the request URLs are
// built at runtime from the env values — we deliberately never log those URLs.

const (
	wxTokenURL  = "https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s"
	wxTicketURL = "https://api.weixin.qq.com/cgi-bin/ticket/getticket?access_token=%s&type=jsapi"
)

// WeChatJSSDK caches the jsapi_ticket and signs wx.config payloads. The ticket
// is valid for 7200s and rate-limited, so it MUST be cached process-wide.
type WeChatJSSDK struct {
	appID     string
	appSecret string
	client    *http.Client

	mu        sync.Mutex
	ticket    string
	ticketExp time.Time
}

// NewWeChatJSSDK returns a manager, or nil when credentials are absent — a nil
// manager means the feature is simply disabled (no share script injected, the
// config endpoint 503s).
func NewWeChatJSSDK(appID, appSecret string) *WeChatJSSDK {
	if appID == "" || appSecret == "" {
		return nil
	}
	return &WeChatJSSDK{
		appID:     appID,
		appSecret: appSecret,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// wxConfig is the signed payload the page passes to wx.config.
type wxConfig struct {
	AppID     string `json:"appId"`
	Timestamp int64  `json:"timestamp"`
	NonceStr  string `json:"nonceStr"`
	Signature string `json:"signature"`
}

// Sign builds a wx.config payload for the exact page URL (which must already be
// stripped of any #fragment by the caller — WeChat signs the full URL sans hash).
func (w *WeChatJSSDK) Sign(pageURL string) (*wxConfig, error) {
	ticket, err := w.jsapiTicket()
	if err != nil {
		return nil, err
	}
	nonce := randHex(16)
	ts := time.Now().Unix()
	// Fields are concatenated in ASCII (dictionary) order: jsapi_ticket,
	// noncestr, timestamp, url — then SHA1'd. This order is already sorted.
	raw := fmt.Sprintf("jsapi_ticket=%s&noncestr=%s&timestamp=%d&url=%s", ticket, nonce, ts, pageURL)
	sum := sha1.Sum([]byte(raw))
	return &wxConfig{
		AppID:     w.appID,
		Timestamp: ts,
		NonceStr:  nonce,
		Signature: hex.EncodeToString(sum[:]),
	}, nil
}

// jsapiTicket returns a cached ticket, refreshing it (and the access_token it
// depends on) when within 5 minutes of expiry.
func (w *WeChatJSSDK) jsapiTicket() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ticket != "" && time.Now().Before(w.ticketExp) {
		return w.ticket, nil
	}
	token, err := w.fetchAccessToken()
	if err != nil {
		return "", err
	}
	ticket, expiresIn, err := w.fetchTicket(token)
	if err != nil {
		return "", err
	}
	w.ticket = ticket
	w.ticketExp = time.Now().Add(time.Duration(expiresIn-300) * time.Second)
	return w.ticket, nil
}

func (w *WeChatJSSDK) fetchAccessToken() (string, error) {
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := w.getJSON(fmt.Sprintf(wxTokenURL, url.QueryEscape(w.appID), url.QueryEscape(w.appSecret)), &body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("access_token errcode=%d errmsg=%s", body.ErrCode, body.ErrMsg)
	}
	return body.AccessToken, nil
}

func (w *WeChatJSSDK) fetchTicket(token string) (string, int, error) {
	var body struct {
		Ticket    string `json:"ticket"`
		ExpiresIn int    `json:"expires_in"`
		ErrCode   int    `json:"errcode"`
		ErrMsg    string `json:"errmsg"`
	}
	if err := w.getJSON(fmt.Sprintf(wxTicketURL, url.QueryEscape(token)), &body); err != nil {
		return "", 0, err
	}
	if body.Ticket == "" {
		return "", 0, fmt.Errorf("jsapi_ticket errcode=%d errmsg=%s", body.ErrCode, body.ErrMsg)
	}
	if body.ExpiresIn <= 0 {
		body.ExpiresIn = 7200
	}
	return body.Ticket, body.ExpiresIn, nil
}

// getJSON GETs u and decodes the JSON body. u may embed the appSecret, so it is
// never logged here or by callers.
func (w *WeChatJSSDK) getJSON(u string, v interface{}) error {
	resp, err := w.client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

// HandleWeChatJSConfig signs a wx.config payload for the page URL in ?url=.
// Public (shared pages carry no auth). GET /wx/js-config?url=<page-url>
func (s *Server) HandleWeChatJSConfig(w http.ResponseWriter, r *http.Request) {
	if s.WeChat == nil {
		writeError(w, http.StatusServiceUnavailable, "wechat jssdk not configured")
		return
	}
	pageURL := r.URL.Query().Get("url")
	if pageURL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	// Only sign URLs on our own public host so this can't be abused as a signing
	// oracle for arbitrary third-party pages.
	parsed, err := url.Parse(pageURL)
	if err != nil || parsed.Host == "" {
		writeError(w, http.StatusBadRequest, "invalid url")
		return
	}
	expectedHost := r.Header.Get("X-Forwarded-Host")
	if expectedHost == "" {
		expectedHost = r.Host
	}
	if !strings.EqualFold(parsed.Host, expectedHost) {
		writeError(w, http.StatusForbidden, "url host not allowed")
		return
	}
	cfg, err := s.WeChat.Sign(pageURL)
	if err != nil {
		log.Printf("[api] wechat js-config sign failed: %v", err)
		writeError(w, http.StatusBadGateway, "failed to sign wechat config")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}
