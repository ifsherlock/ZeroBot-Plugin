package mediaparser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

const cardImageAccept = "image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8"

type imageHeaderMode uint8

const (
	imageHeadersOriginal imageHeaderMode = iota
	imageHeadersNoReferrer
	imageHeadersSourceOrigin
)

func fetchText(raw string, headers map[string]string, redirects bool) (string, string, int, error) {
	return fetchTextWithClient(raw, headers, redirects, nil)
}

func fetchTextWithPlatform(cfg config, platform, raw string, headers map[string]string, redirects bool) (string, string, int, error) {
	return fetchTextWithClient(raw, headers, redirects, httpClientForPlatform(cfg, platform, 45*time.Second, redirects))
}

func fetchTextWithClient(raw string, headers map[string]string, redirects bool, c *http.Client) (string, string, int, error) {
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return "", raw, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", defaultUA)
	}
	if c == nil {
		c = &http.Client{Timeout: 45 * time.Second}
	}
	if !redirects {
		c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", raw, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return "", resp.Request.URL.String(), resp.StatusCode, err
	}
	return string(body), resp.Request.URL.String(), resp.StatusCode, nil
}

func redirectURL(raw string, headers map[string]string) (string, error) {
	return redirectURLWithClient(raw, headers, nil)
}

func redirectURLWithPlatform(cfg config, platform, raw string, headers map[string]string) (string, error) {
	return redirectURLWithClient(raw, headers, httpClientForPlatform(cfg, platform, 25*time.Second, true))
}

func redirectURLWithClient(raw string, headers map[string]string, c *http.Client) (string, error) {
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", defaultUA)
	}
	if c == nil {
		c = &http.Client{Timeout: 25 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String(), nil
	}
	return raw, nil
}

func proxyAllowedPlatform(platform string) bool {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "twitter", "tiktok", "youtube", "instagram", "linuxdo":
		return true
	default:
		return false
	}
}

func proxyForPlatform(cfg config, platform string) string {
	if !proxyAllowedPlatform(platform) {
		return ""
	}
	return strings.TrimSpace(cfg.Proxy)
}

func httpClientForPlatform(cfg config, platform string, timeout time.Duration, redirects bool) *http.Client {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	c := &http.Client{Timeout: timeout}
	if !redirects {
		c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	rawProxy := proxyForPlatform(cfg, platform)
	if rawProxy == "" {
		return c
	}
	transport, err := transportForProxy(rawProxy)
	if err != nil {
		logDebug(cfg, "proxy ignored platform=%s proxy=%q error=%v", platform, rawProxy, err)
		return c
	}
	c.Transport = transport
	return c
}

func imageHeaderAttempts(raw string, headers map[string]string) []imageHeaderMode {
	source := trustedImageSource(headers)
	target, err := url.Parse(raw)
	if err == nil && sameHTTPOrigin(target, source) {
		return []imageHeaderMode{imageHeadersOriginal, imageHeadersNoReferrer, imageHeadersSourceOrigin}
	}
	if source != nil {
		return []imageHeaderMode{imageHeadersNoReferrer, imageHeadersSourceOrigin}
	}
	return []imageHeaderMode{imageHeadersNoReferrer}
}

func doImageRequest(baseClient *http.Client, raw string, headers map[string]string, mode imageHeaderMode) (*http.Response, error) {
	if baseClient == nil {
		baseClient = &http.Client{Timeout: 18 * time.Second}
	}
	client := *baseClient
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		req.Header = imageRequestHeaders(req.URL, headers, mode)
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header = imageRequestHeaders(req.URL, headers, mode)
	return client.Do(req)
}

func imageRequestHeaders(target *url.URL, headers map[string]string, mode imageHeaderMode) http.Header {
	base := make(http.Header, len(headers))
	for key, value := range headers {
		base.Set(key, value)
	}
	source := trustedImageSource(headers)
	sameOrigin := sameHTTPOrigin(target, source)
	out := make(http.Header)

	if sameOrigin && (mode == imageHeadersOriginal || mode == imageHeadersNoReferrer) {
		for key, values := range base {
			if imageHopByHopHeader(key) {
				continue
			}
			out[key] = append([]string(nil), values...)
		}
		if mode == imageHeadersNoReferrer {
			out.Del("Referer")
			out.Del("Origin")
		}
	} else {
		for _, key := range []string{"User-Agent", "Accept-Language", "Cache-Control"} {
			if value := base.Get(key); value != "" {
				out.Set(key, value)
			}
		}
		if mode == imageHeadersSourceOrigin && source != nil {
			out.Set("Referer", httpOrigin(source)+"/")
			if origin, err := url.Parse(base.Get("Origin")); err == nil && sameHTTPOrigin(origin, source) {
				out.Set("Origin", httpOrigin(source))
			}
		}
	}

	if out.Get("User-Agent") == "" {
		out.Set("User-Agent", defaultUA)
	}
	out.Set("Accept", cardImageAccept)
	return out
}

func trustedImageSource(headers map[string]string) *url.URL {
	base := make(http.Header, len(headers))
	for key, value := range headers {
		base.Set(key, value)
	}
	for _, raw := range []string{base.Get("Referer"), base.Get("Origin")} {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err == nil && u.Host != "" && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) {
			return u
		}
	}
	return nil
}

func sameHTTPOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || a.Host == "" || b.Host == "" {
		return false
	}
	if !strings.EqualFold(a.Scheme, b.Scheme) || !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	return httpOriginPort(a) == httpOriginPort(b)
}

func httpOriginPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return ""
}

func httpOrigin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if port != "" && !((strings.EqualFold(u.Scheme, "https") && port == "443") || (strings.EqualFold(u.Scheme, "http") && port == "80")) {
		host += ":" + port
	}
	return strings.ToLower(u.Scheme) + "://" + host
}

func imageHopByHopHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "connection", "keep-alive", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade", "host":
		return true
	default:
		return false
	}
}

func transportForProxy(rawProxy string) (*http.Transport, error) {
	u, err := url.Parse(strings.TrimSpace(rawProxy))
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" {
		u, err = url.Parse("http://" + strings.TrimSpace(rawProxy))
		if err != nil {
			return nil, err
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h":
		if strings.EqualFold(u.Scheme, "socks5h") {
			u.Scheme = "socks5"
		}
		dialer, err := xproxy.FromURL(u, xproxy.Direct)
		if err != nil {
			return nil, err
		}
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			type contextDialer interface {
				DialContext(context.Context, string, string) (net.Conn, error)
			}
			if d, ok := dialer.(contextDialer); ok {
				return d.DialContext(ctx, network, address)
			}
			type dialResult struct {
				conn net.Conn
				err  error
			}
			ch := make(chan dialResult, 1)
			go func() {
				conn, err := dialer.Dial(network, address)
				ch <- dialResult{conn: conn, err: err}
			}()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case res := <-ch:
				return res.conn, res.err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	return transport, nil
}

func extractAssignedJSONObject(html, marker string) (map[string]any, error) {
	idx := strings.Index(html, marker)
	if idx < 0 {
		return nil, fmt.Errorf("未找到 %s", marker)
	}
	start := strings.Index(html[idx:], "{")
	if start < 0 {
		return nil, fmt.Errorf("未找到 JSON 开始位置")
	}
	start += idx
	end := findBalancedJSONEnd(html, start)
	if end <= start {
		return nil, fmt.Errorf("未找到完整 JSON")
	}
	raw := regexp.MustCompile(`\bundefined\b`).ReplaceAllString(html[start:end], "null")
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func extractScriptJSON(html, scriptID string) string {
	pattern := `(?is)<script[^>]+id=["']` + regexp.QuoteMeta(scriptID) + `["'][^>]*>(.*?)</script>`
	m := regexp.MustCompile(pattern).FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	return htmlUnescape(strings.TrimSpace(m[1]))
}

func findBalancedJSONEnd(s string, start int) int {
	depth := 0
	inString := false
	quote := byte(0)
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inString {
			if ch == '\\' {
				escaped = true
			} else if ch == quote {
				inString = false
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = true
			quote = ch
			continue
		}
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func getMap(v any, keys ...string) map[string]any {
	cur := v
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	m, _ := cur.(map[string]any)
	return m
}

func getSlice(v any, keys ...string) []any {
	cur := v
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	a, _ := cur.([]any)
	return a
}

func getString(v any, keys ...string) string {
	cur := v
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[key]
	}
	switch x := cur.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	case json.Number:
		return x.String()
	default:
		return ""
	}
}

func getFloat(v any, keys ...string) float64 {
	cur := v
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0
		}
		cur = m[key]
	}
	switch x := cur.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case string:
		return parseFloat(x)
	default:
		return 0
	}
}

func parseFloat(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	return f
}

func nestedHTTPURLs(v any, maxDepth int) []string {
	seen := map[string]bool{}
	out := []string{}
	var walk func(any, int)
	walk = func(x any, depth int) {
		if depth > maxDepth || x == nil {
			return
		}
		switch val := x.(type) {
		case string:
			decoded := strings.ReplaceAll(strings.ReplaceAll(val, `\u002F`, "/"), `\/`, "/")
			for _, raw := range regexp.MustCompile(`https?://[^\s<>"']+`).FindAllString(decoded, -1) {
				raw = strings.TrimRight(raw, `.,!?)]}>"'，。！？；：）】》」`)
				if !seen[raw] {
					seen[raw] = true
					out = append(out, raw)
				}
			}
		case []any:
			for _, item := range val {
				walk(item, depth+1)
			}
		case map[string]any:
			preferred := []string{"urlList", "url_list", "urls", "url", "playAddr", "downloadAddr", "PlayAddrStruct", "imageURL", "imageUrl", "originImage", "downloadImage", "cover"}
			for _, key := range preferred {
				if _, ok := val[key]; ok {
					walk(val[key], depth+1)
				}
			}
			for _, item := range val {
				walk(item, depth+1)
			}
		}
	}
	walk(v, 0)
	return out
}

func firstNestedHTTPURL(v any, maxDepth int) string {
	for _, u := range nestedHTTPURLs(v, maxDepth) {
		if u != "" {
			return ensureHTTPS(u)
		}
	}
	return ""
}

func firstNestedHTTPURLByKeys(v any, maxDepth int, keys ...string) string {
	needles := make([]string, 0, len(keys))
	for _, key := range keys {
		if key = strings.ToLower(strings.TrimSpace(key)); key != "" {
			needles = append(needles, key)
		}
	}
	var walk func(any, int) string
	walk = func(x any, depth int) string {
		if x == nil || depth > maxDepth {
			return ""
		}
		switch val := x.(type) {
		case map[string]any:
			for key, item := range val {
				lower := strings.ToLower(key)
				for _, needle := range needles {
					if strings.Contains(lower, needle) {
						if u := firstNestedHTTPURL(item, 4); u != "" {
							return u
						}
					}
				}
			}
			for _, item := range val {
				if u := walk(item, depth+1); u != "" {
					return u
				}
			}
		case []any:
			for _, item := range val {
				if u := walk(item, depth+1); u != "" {
					return u
				}
			}
		}
		return ""
	}
	return walk(v, 0)
}

func buildHeaders(isVideo bool, referer, ua string) map[string]string {
	if ua == "" {
		ua = defaultUA
	}
	headers := map[string]string{
		"User-Agent":      ua,
		"Accept-Language": "zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7",
	}
	if isVideo {
		headers["Accept"] = "*/*"
	} else {
		headers["Accept"] = "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8"
	}
	if referer != "" {
		headers["Referer"] = referer
	}
	return headers
}

func ensureHTTPS(raw string) string {
	switch {
	case strings.HasPrefix(raw, "//"):
		return "https:" + raw
	case strings.HasPrefix(raw, "http://"):
		return "https://" + strings.TrimPrefix(raw, "http://")
	default:
		return raw
	}
}

func cleanURLQuery(raw string, drop ...string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	for _, key := range drop {
		q.Del(key)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
