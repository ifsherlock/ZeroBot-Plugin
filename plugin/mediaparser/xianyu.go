package mediaparser

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	xianyuUA           = "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/135.0.0.0 Mobile Safari/537.36"
	xianyuAppKey       = "34839810"
	xianyuMtopBase     = "https://h5api.m.goofish.com"
	xianyuDetailAPI    = "mtop.taobao.idle.awesome.detail"
	xianyuDetailAPIVer = "1.0"
)

func parseXianyu(cfg config, raw string) (mediaMeta, error) {
	ctx, err := resolveXianyuContext(raw)
	if err != nil {
		return mediaMeta{}, err
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Timeout: 45 * time.Second, Jar: jar}
	detail, err := callXianyuDetail(c, ctx.ItemID, ctx.MobileURL)
	if err != nil {
		return mediaMeta{}, err
	}
	return buildXianyuMeta(ctx, detail)
}

type xianyuContext struct {
	ItemID    string
	SourceURL string
	MobileURL string
	PCURL     string
}

func resolveXianyuContext(raw string) (xianyuContext, error) {
	source := raw
	itemID := xianyuItemID(raw)
	mobileURL := ""
	pcURL := ""
	host := hostOf(raw)
	if host == "m.tb.cn" {
		headers := map[string]string{"User-Agent": xianyuUA}
		html, finalURL, _, err := fetchText(raw, headers, true)
		if err != nil {
			return xianyuContext{}, err
		}
		redirect := extractXianyuRedirect(html)
		candidate := firstNonEmpty(redirect, finalURL)
		if !strings.Contains(hostOf(candidate), "goofish.com") {
			return xianyuContext{}, fmt.Errorf("短链未展开为闲鱼商品页")
		}
		itemID = firstNonEmpty(xianyuItemID(candidate), itemID)
		if hostOf(candidate) == "h5.m.goofish.com" {
			mobileURL = candidate
		} else {
			pcURL = candidate
		}
	} else if strings.Contains(host, "goofish.com") {
		if host == "h5.m.goofish.com" {
			mobileURL = raw
		} else {
			pcURL = raw
		}
	}
	if itemID == "" {
		return xianyuContext{}, fmt.Errorf("无法从闲鱼链接中提取商品 id")
	}
	if mobileURL == "" {
		mobileURL = "https://h5.m.goofish.com/item?id=" + itemID + "&itemId=" + itemID
	}
	if pcURL == "" {
		pcURL = "https://www.goofish.com/item?id=" + itemID
	}
	return xianyuContext{ItemID: itemID, SourceURL: source, MobileURL: mobileURL, PCURL: pcURL}, nil
}

func xianyuItemID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	for _, key := range []string{"id", "itemId", "item_id"} {
		for _, value := range q[key] {
			if regexp.MustCompile(`^\d{8,20}$`).MatchString(value) {
				return value
			}
		}
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if regexp.MustCompile(`^\d{8,20}$`).MatchString(seg) {
			return seg
		}
	}
	return ""
}

func extractXianyuRedirect(html string) string {
	patterns := []string{
		`var\s+url\s*=\s*'([^']+)'`,
		`var\s+url\s*=\s*"([^"]+)"`,
		`window\.location(?:\.replace)?\((['"])(https?://[^'"]+)\1\)`,
	}
	for _, p := range patterns {
		m := regexp.MustCompile(p).FindStringSubmatch(html)
		if len(m) > 2 {
			if decoded, err := url.QueryUnescape(htmlUnescape(m[2])); err == nil {
				return strings.ReplaceAll(strings.ReplaceAll(decoded, `\u002F`, "/"), `\/`, "/")
			}
			return m[2]
		}
		if len(m) > 1 {
			if decoded, err := url.QueryUnescape(htmlUnescape(m[1])); err == nil {
				return strings.ReplaceAll(strings.ReplaceAll(decoded, `\u002F`, "/"), `\/`, "/")
			}
			return m[1]
		}
	}
	return ""
}

func callXianyuDetail(c *http.Client, itemID, referer string) (map[string]any, error) {
	dataBytes, _ := json.Marshal(map[string]string{"itemId": itemID})
	dataStr := string(dataBytes)
	apiURL := xianyuMtopBase + "/h5/" + xianyuDetailAPI + "/" + xianyuDetailAPIVer + "/"
	headers := map[string]string{
		"User-Agent":      xianyuUA,
		"Accept":          "application/json",
		"Accept-Language": "zh-CN,zh;q=0.9",
		"Origin":          "https://h5.m.goofish.com",
		"Referer":         referer,
	}
	token := xianyuToken(c)
	if token == "" {
		_ = xianyuMtopPost(c, apiURL, xianyuMtopParams("", timestampMS(), xianyuDetailAPI, xianyuDetailAPIVer), dataStr, headers, nil)
		token = xianyuToken(c)
	}
	if token == "" {
		return nil, fmt.Errorf("无法获取闲鱼 MTop 令牌")
	}
	for attempt := 0; attempt < 2; attempt++ {
		t := timestampMS()
		sum := md5.Sum([]byte(token + "&" + t + "&" + xianyuAppKey + "&" + dataStr))
		params := xianyuMtopParams(hex.EncodeToString(sum[:]), t, xianyuDetailAPI, xianyuDetailAPIVer)
		var payload map[string]any
		if err := xianyuMtopPost(c, apiURL, params, dataStr, headers, &payload); err != nil {
			return nil, err
		}
		ret := fmt.Sprint(payload["ret"])
		if strings.Contains(ret, "FAIL_SYS_TOKEN") && attempt == 0 {
			_ = xianyuMtopPost(c, apiURL, xianyuMtopParams("", timestampMS(), xianyuDetailAPI, xianyuDetailAPIVer), dataStr, headers, nil)
			token = xianyuToken(c)
			continue
		}
		if ret != "" && !strings.Contains(ret, "SUCCESS") {
			return nil, fmt.Errorf("闲鱼详情接口返回失败: %s", ret)
		}
		if data := getMap(payload, "data"); data != nil {
			return data, nil
		}
		return nil, fmt.Errorf("闲鱼详情接口返回空数据")
	}
	return nil, fmt.Errorf("闲鱼详情接口请求失败")
}

func xianyuMtopParams(sign, t, api, ver string) url.Values {
	q := url.Values{}
	q.Set("jsv", "2.7.2")
	q.Set("appKey", xianyuAppKey)
	q.Set("t", t)
	q.Set("sign", sign)
	q.Set("v", ver)
	q.Set("type", "originaljson")
	q.Set("accountSite", "xianyu")
	q.Set("dataType", "json")
	q.Set("timeout", "20000")
	q.Set("api", api)
	q.Set("sessionOption", "AutoLoginOnly")
	return q
}

func xianyuMtopPost(c *http.Client, api string, params url.Values, dataStr string, headers map[string]string, out any) error {
	req, err := http.NewRequest(http.MethodPost, api+"?"+params.Encode(), strings.NewReader(url.Values{"data": {dataStr}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

func xianyuToken(c *http.Client) string {
	u, _ := url.Parse(xianyuMtopBase)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == "_m_h5_tk" {
			return strings.SplitN(ck.Value, "_", 2)[0]
		}
	}
	return ""
}

func buildXianyuMeta(ctx xianyuContext, detail map[string]any) (mediaMeta, error) {
	item := getMap(detail, "itemDO")
	title := getString(item, "title")
	if title == "" {
		return mediaMeta{}, fmt.Errorf("闲鱼详情缺少标题")
	}
	seller := getMap(detail, "sellerDO")
	sellerName := firstNonEmpty(getString(seller, "nick"), getString(seller, "desensitizationNick"))
	sellerID := getString(seller, "sellerId")
	author := sellerName
	if sellerID != "" {
		if author != "" {
			author += "(主页id:" + sellerID + ")"
		} else {
			author = "(主页id:" + sellerID + ")"
		}
	}
	imageGroups := [][]string{}
	for _, img := range getSlice(item, "imageInfos") {
		if m, ok := img.(map[string]any); ok {
			if u := ensureHTTPS(getString(m, "url")); u != "" {
				imageGroups = append(imageGroups, []string{u})
			}
		}
	}
	videoCandidates := []string{}
	var walk func(any, string)
	walk = func(v any, hint string) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				walk(vv, strings.ToLower(k))
			}
		case []any:
			for _, vv := range x {
				walk(vv, hint)
			}
		case string:
			if !strings.Contains(hint, "video") && !strings.Contains(hint, "play") && !strings.Contains(hint, "stream") && !strings.Contains(hint, "url") {
				return
			}
			for _, u := range nestedHTTPURLs(x, 1) {
				l := strings.ToLower(u)
				if strings.Contains(l, ".mp4") || strings.Contains(l, ".m3u8") || strings.Contains(l, "/play/") || strings.Contains(l, "video") {
					videoCandidates = append(videoCandidates, ensureHTTPS(u))
				}
			}
		}
	}
	walk(detail, "")
	videoGroups := [][]string{}
	if uniq := uniqueStrings(videoCandidates); len(uniq) > 0 {
		videoGroups = append(videoGroups, uniq)
	}
	cover := ""
	if len(imageGroups) > 0 && len(imageGroups[0]) > 0 {
		cover = imageGroups[0][0]
	}
	if cover == "" {
		cover = firstNestedHTTPURLByKeys(detail, 6, "cover", "pic", "image")
	}
	desc := buildXianyuDesc(detail)
	return mediaMeta{
		URL:        ctx.SourceURL,
		SourceURL:  ctx.SourceURL,
		Platform:   "xianyu",
		Title:      title,
		Author:     author,
		Avatar:     firstNestedHTTPURLByKeys(seller, 5, "avatar", "head", "logo"),
		Desc:       desc,
		Timestamp:  formatAnyTimestamp(getFloat(item, "gmtCreate")),
		Cover:      cover,
		VideoURLs:  videoGroups,
		ImageURLs:  imageGroups,
		VideoHeads: buildHeaders(true, ctx.MobileURL, xianyuUA),
		ImageHeads: buildHeaders(false, ctx.MobileURL, xianyuUA),
	}, nil
}

func buildXianyuDesc(detail map[string]any) string {
	item := getMap(detail, "itemDO")
	seller := getMap(detail, "sellerDO")
	lines := []string{}
	if price := getString(item, "soldPrice"); price != "" {
		lines = append(lines, "价格：¥"+price+getString(item, "priceUnit"))
	}
	if fee := getString(item, "transportFee"); fee != "" {
		lines = append(lines, "运费：¥"+fee)
	}
	if loc := firstNonEmpty(getString(seller, "publishCity"), getString(seller, "city")); loc != "" {
		lines = append(lines, "位置："+loc)
	}
	if desc := getString(item, "desc"); desc != "" {
		lines = append(lines, desc)
	}
	return strings.Join(uniqueStrings(lines), "\n")
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.Trim(u.Hostname(), "."))
}

func timestampMS() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 10)
}
