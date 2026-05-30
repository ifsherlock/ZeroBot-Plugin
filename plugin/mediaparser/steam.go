package mediaparser

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/url"
	"regexp"
	"strings"
)

var steamReviewsAPIBase = "https://store.steampowered.com/appreviews"

func parseSteam(cfg config, raw string) (mediaMeta, error) {
	appID := steamAppID(raw)
	if appID == "" {
		return mediaMeta{}, fmt.Errorf("steam app id not found")
	}
	meta := mediaMeta{
		URL:        raw,
		SourceURL:  raw,
		Platform:   "steam",
		SteamAppID: appID,
	}

	detail, err := fetchSteamAppDetails(appID)
	if err != nil {
		return meta, err
	}
	meta.Title = firstNonEmpty(getString(detail, "name"), "Steam "+appID)
	meta.Desc = steamCleanText(firstNonEmpty(getString(detail, "short_description"), getString(detail, "about_the_game")))
	meta.Cover = firstNonEmpty(getString(detail, "library_capsule"), getString(detail, "capsule_imagev5"), getString(detail, "capsule_image"), getString(detail, "header_image"))
	meta.SteamHeaderImage = firstNonEmpty(getString(detail, "header_image"), meta.Cover)
	meta.SteamSubtitle = steamSubtitleFromURL(raw)
	meta.SteamGenres = steamGenres(detail)

	price := getMap(detail, "price_overview")
	meta.SteamPriceCurrent = firstNonEmpty(getString(price, "final_formatted"), steamFormatCNY(getFloat(price, "final")))
	meta.SteamPriceOriginal = firstNonEmpty(getString(price, "initial_formatted"), steamFormatCNY(getFloat(price, "initial")))
	meta.SteamDiscount = int(getFloat(price, "discount_percent"))
	if meta.SteamPriceCurrent == "" && getFloat(detail, "is_free") > 0 {
		meta.SteamPriceCurrent = "免费"
	}

	percent, summary := fetchSteamReviewSummary(appID)
	meta.SteamReviewPercent = percent
	meta.SteamReviewSummary = summary
	return meta, nil
}

func steamAppID(raw string) string {
	if id := keylolSteamAppID(raw); id != "" {
		return id
	}
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if !strings.Contains(strings.ToLower(u.Host), "store.steampowered.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && strings.EqualFold(parts[0], "app") && regexp.MustCompile(`^\d+$`).MatchString(parts[1]) {
		return parts[1]
	}
	return ""
}

func fetchSteamAppDetails(appID string) (map[string]any, error) {
	api, err := url.Parse(steamAPIBase)
	if err != nil {
		return nil, err
	}
	q := api.Query()
	q.Set("appids", appID)
	q.Set("cc", "cn")
	q.Set("l", "zh")
	api.RawQuery = q.Encode()
	body, _, status, err := fetchText(api.String(), map[string]string{"Accept": "application/json"}, true)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("steam API HTTP %d", status)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return nil, err
	}
	app := getMap(data, appID)
	success, _ := app["success"].(bool)
	if !success {
		return nil, fmt.Errorf("steam appdetails failed")
	}
	return getMap(app, "data"), nil
}

func fetchSteamReviewSummary(appID string) (int, string) {
	api, err := url.Parse(strings.TrimRight(steamReviewsAPIBase, "/") + "/" + appID)
	if err != nil {
		return 0, ""
	}
	q := api.Query()
	q.Set("json", "1")
	q.Set("language", "all")
	q.Set("purchase_type", "all")
	q.Set("num_per_page", "0")
	api.RawQuery = q.Encode()
	body, _, status, err := fetchText(api.String(), map[string]string{"Accept": "application/json"}, true)
	if err != nil || status >= 400 {
		return 0, ""
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return 0, ""
	}
	summary := getMap(data, "query_summary")
	total := getFloat(summary, "total_reviews")
	positive := getFloat(summary, "total_positive")
	percent := 0
	if total > 0 {
		percent = int(math.Round(positive * 100 / total))
	}
	return percent, getString(summary, "review_score_desc")
}

func steamGenres(detail map[string]any) []string {
	out := make([]string, 0, 4)
	for _, raw := range getSlice(detail, "genres") {
		if item, ok := raw.(map[string]any); ok {
			if s := strings.TrimSpace(getString(item, "description")); s != "" {
				out = append(out, s)
			}
		}
		if len(out) >= 4 {
			break
		}
	}
	return out
}

func steamSubtitleFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 {
		return ""
	}
	name, _ := url.PathUnescape(parts[2])
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	name = strings.TrimSpace(name)
	if regexp.MustCompile(`^\d+$`).MatchString(name) {
		return ""
	}
	return name
}

func steamCleanText(s string) string {
	s = regexp.MustCompile(`(?s)<[^>]+>`).ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func steamFormatCNY(v float64) string {
	if v <= 0 {
		return ""
	}
	return fmt.Sprintf("¥ %.2f", v/100)
}
