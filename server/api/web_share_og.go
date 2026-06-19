package api

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Open Graph injection for shared HTML pages.
//
// WeChat/QQ/Lark render a link as a rich card only when they can crawl a valid
// set of Open Graph tags — crucially a publicly fetchable og:image. Authors
// rarely fill these in correctly (placeholder domains, missing absolute URLs),
// so when we serve a shared page we rewrite its <head> to guarantee a correct
// card: og:url always points at the real share link, og:image falls back to a
// platform default, and og:title/description are lifted from the page when the
// author didn't provide og: equivalents. The author's source file is never
// touched — all of this happens at serve time.

var (
	metaTagRe   = regexp.MustCompile(`(?is)<meta\b[^>]*?>`)
	attrRe      = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*"([^"]*)"|([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*'([^']*)'`)
	titleRe     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	headOpenRe  = regexp.MustCompile(`(?is)<head\b[^>]*>`)
	asciiHostRe = regexp.MustCompile(`^[a-zA-Z0-9.\-:]+$`)
)

// managedOGProps are the og:* properties we own. Any existing tag for these is
// stripped from the page so our injected block is the single source of truth
// (duplicate og:* tags lead to crawlers picking the stale/placeholder one).
var managedOGProps = map[string]bool{
	"og:type":        true,
	"og:title":       true,
	"og:description": true,
	"og:url":         true,
	"og:image":       true,
	"og:site_name":   true,
}

// parseMetaAttrs extracts attribute name→value pairs from a single <meta> tag.
func parseMetaAttrs(tag string) map[string]string {
	attrs := make(map[string]string)
	for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
		if m[1] != "" {
			attrs[strings.ToLower(m[1])] = m[2]
		} else if m[3] != "" {
			attrs[strings.ToLower(m[3])] = m[4]
		}
	}
	return attrs
}

// isFetchableImageURL reports whether s is an absolute http(s) URL with a
// real-looking host — i.e. something a remote crawler can actually GET. This
// rejects relative paths and placeholder domains like "你的域名" (non-ASCII host).
func isFetchableImageURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" || !strings.Contains(u.Host, ".") {
		return false
	}
	return asciiHostRe.MatchString(u.Host)
}

// publicBaseURL reconstructs the externally-visible scheme://host for the
// request, honouring the reverse proxy in front of us (box-nex.top terminates
// TLS and forwards over plain HTTP, so r.TLS is nil and we must trust the
// X-Forwarded-* headers it sets).
func publicBaseURL(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// injectOpenGraph rewrites an HTML document's <head> so the page renders as a
// rich link card. shareURL is the canonical public share link and defaultImage
// the fallback og:image. Returns the original bytes unchanged if there is no
// <head> to inject into.
func injectOpenGraph(htmlContent, shareURL, defaultImage string) string {
	headLoc := headOpenRe.FindStringIndex(htmlContent)
	if headLoc == nil {
		return htmlContent // not a document we can safely rewrite
	}

	// First pass: read existing og:* / name= values for fallbacks and collect
	// the managed og:* tags to strip.
	existing := make(map[string]string)
	var stripTags []string
	for _, tag := range metaTagRe.FindAllString(htmlContent, -1) {
		attrs := parseMetaAttrs(tag)
		if prop := strings.ToLower(attrs["property"]); managedOGProps[prop] {
			existing[prop] = attrs["content"]
			stripTags = append(stripTags, tag)
		} else if name := strings.ToLower(attrs["name"]); name == "description" {
			if _, ok := existing["meta:description"]; !ok {
				existing["meta:description"] = attrs["content"]
			}
		}
	}

	// Resolve each managed property: prefer the author's value when it's
	// usable, otherwise fall back.
	title := firstNonEmpty(existing["og:title"], extractTitle(htmlContent), "分享页面")
	desc := firstNonEmpty(existing["og:description"], existing["meta:description"])
	siteName := firstNonEmpty(existing["og:site_name"], "NexBox")
	image := existing["og:image"]
	if !isFetchableImageURL(image) {
		image = defaultImage
	}
	ogType := firstNonEmpty(existing["og:type"], "website")

	// Build the injected block. og:url is always the real share link.
	var b strings.Builder
	b.WriteString("\n")
	writeMeta(&b, "og:type", ogType)
	writeMeta(&b, "og:title", title)
	if desc != "" {
		writeMeta(&b, "og:description", desc)
	}
	writeMeta(&b, "og:url", shareURL)
	writeMeta(&b, "og:image", image)
	writeMeta(&b, "og:site_name", siteName)

	// Strip the author's managed og:* tags so ours are authoritative.
	out := htmlContent
	for _, tag := range stripTags {
		out = strings.Replace(out, tag, "", 1)
	}

	// Re-locate <head> in the stripped output and insert our block right after it.
	loc := headOpenRe.FindStringIndex(out)
	if loc == nil {
		return htmlContent
	}
	return out[:loc[1]] + b.String() + out[loc[1]:]
}

func writeMeta(b *strings.Builder, prop, content string) {
	b.WriteString(`  <meta property="`)
	b.WriteString(prop)
	b.WriteString(`" content="`)
	b.WriteString(html.EscapeString(content))
	b.WriteString("\">\n")
}

// wechatShareScript bootstraps WeChat JS-SDK custom share on the page: it loads
// jweixin, asks our backend to sign the current URL, configures wx, and sets the
// share card from the og:* tags injectOpenGraph already wrote. Pure client-side
// progressive enhancement — it no-ops outside WeChat and never blocks rendering.
const wechatShareScript = `
<script>
(function(){
  var page = location.href.split('#')[0];
  var dbg = /[?&]wxdebug=1/.test(location.search);
  function meta(p){var m=document.querySelector('meta[property="'+p+'"]');return m?m.getAttribute('content'):'';}
  var s=document.createElement('script');
  s.src='https://res.wx.qq.com/open/js/jweixin-1.6.0.js';
  s.onerror=function(){if(dbg)alert('failed to load jweixin');};
  s.onload=function(){
    if(typeof wx==='undefined'){if(dbg)alert('wx undefined');return;}
    fetch('/wx/js-config?url='+encodeURIComponent(page)).then(function(r){return r.ok?r.json():null;}).then(function(c){
      if(!c||!c.signature){if(dbg)alert('no signature from /wx/js-config');return;}
      wx.error(function(res){if(dbg)alert('wx.config error: '+JSON.stringify(res));});
      wx.config({debug:dbg,appId:c.appId,timestamp:c.timestamp,nonceStr:c.nonceStr,signature:c.signature,jsApiList:['updateAppMessageShareData','updateTimelineShareData']});
      wx.ready(function(){
        var title=meta('og:title')||document.title;
        var desc=meta('og:description');
        var img=meta('og:image');
        if(wx.updateAppMessageShareData)wx.updateAppMessageShareData({title:title,desc:desc,link:page,imgUrl:img});
        if(wx.updateTimelineShareData)wx.updateTimelineShareData({title:title,link:page,imgUrl:img});
      });
    }).catch(function(e){if(dbg)alert('js-config fetch failed: '+e);});
  };
  document.head.appendChild(s);
})();
</script>
`

// injectWeChatShare appends the WeChat share bootstrap just before </body> (or
// at the end if there is no body tag). Returns the input unchanged if it has
// already been injected.
func injectWeChatShare(htmlContent string) string {
	if strings.Contains(htmlContent, "/wx/js-config") {
		return htmlContent
	}
	if idx := strings.LastIndex(strings.ToLower(htmlContent), "</body>"); idx != -1 {
		return htmlContent[:idx] + wechatShareScript + htmlContent[idx:]
	}
	return htmlContent + wechatShareScript
}

func extractTitle(htmlContent string) string {
	if m := titleRe.FindStringSubmatch(htmlContent); m != nil {
		return strings.TrimSpace(html.UnescapeString(m[1]))
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
