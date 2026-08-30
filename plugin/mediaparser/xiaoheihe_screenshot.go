package mediaparser

import (
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const (
	xiaoheiheBrowserEnv              = "XIAOHEIHE_MOBILE_SCREENSHOT"
	xiaoheiheBrowserPathEnv          = "XIAOHEIHE_BROWSER_PATH"
	xiaoheiheMobileWidth             = 430
	xiaoheiheMobileViewportHeight    = 932
	xiaoheiheMobileMaxCaptureHeight  = 10000
	xiaoheiheMobileDeviceScale       = 2
	xiaoheiheMobileScreenshotWait    = 7 * time.Second
	xiaoheiheMobileScreenshotTimeout = 60 * time.Second
	xiaoheiheMobileScrollPause       = 180 * time.Millisecond
	xiaoheiheMobileImageWait         = 5 * time.Second
	xiaoheiheMobileUserAgent         = "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36"
)

var xiaoheiheBrowserLaunchMu sync.Mutex

func xiaoheiheMobileScreenshotEnabled() bool {
	value := strings.TrimSpace(os.Getenv(xiaoheiheBrowserEnv))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func renderXiaoheiheMobileScreenshot(meta mediaMeta) (string, error) {
	pageURL := strings.TrimSpace(firstNonEmpty(meta.URL, meta.SourceURL))
	if pageURL == "" || xiaoheiheBBSLinkID(pageURL) == "" {
		return "", fmt.Errorf("xiaoheihe BBS link is required for browser screenshot")
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", fmt.Errorf("create screenshot cache: %w", err)
	}
	out, err := filepath.Abs(filepath.Join(cacheDir, "xiaoheihe_mobile_"+cacheName(meta.SourceURL, meta.Platform)+".png"))
	if err != nil {
		return "", fmt.Errorf("resolve screenshot path: %w", err)
	}
	// Serialize requests so the persistent remote browser is not stressed by bursts.
	xiaoheiheBrowserLaunchMu.Lock()
	defer xiaoheiheBrowserLaunchMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), xiaoheiheMobileScreenshotTimeout)
	defer cancel()
	browserCtx, stopBrowser, err := newBrowserScreenshotContext(ctx, snapshotConfig().BrowserCDPURL)
	if err != nil {
		return "", err
	}
	defer stopBrowser()
	var screenshot []byte
	err = chromedp.Run(browserCtx,
		emulation.SetUserAgentOverride(xiaoheiheMobileUserAgent).
			WithAcceptLanguage("zh-CN,zh;q=0.9").
			WithPlatform("Android"),
		emulation.SetDeviceMetricsOverride(xiaoheiheMobileWidth, xiaoheiheMobileViewportHeight, xiaoheiheMobileDeviceScale, true).
			WithScreenWidth(xiaoheiheMobileWidth).
			WithScreenHeight(xiaoheiheMobileViewportHeight),
		chromedp.Navigate(pageURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(xiaoheiheMobileScreenshotWait),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return prepareXiaoheiheMobileScreenshotPage(ctx, meta)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, _, _, _, contentSize, metricsErr := page.GetLayoutMetrics().Do(ctx)
			if metricsErr != nil {
				return metricsErr
			}
			if contentSize == nil || contentSize.Width <= 0 || contentSize.Height <= 0 {
				return fmt.Errorf("browser returned an invalid page size")
			}
			height := min(contentSize.Height, float64(xiaoheiheMobileMaxCaptureHeight))
			var captureErr error
			screenshot, captureErr = page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatPng).
				WithCaptureBeyondViewport(true).
				WithFromSurface(true).
				WithClip(&page.Viewport{X: 0, Y: 0, Width: contentSize.Width, Height: height, Scale: 1}).
				Do(ctx)
			return captureErr
		}),
	)
	if ctx.Err() != nil {
		return "", fmt.Errorf("mobile screenshot timed out after %s: %w", xiaoheiheMobileScreenshotTimeout, ctx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("run mobile browser: %w", err)
	}
	if err := os.WriteFile(out, screenshot, 0644); err != nil {
		return "", fmt.Errorf("write mobile screenshot: %w", err)
	}
	if err := validateXiaoheiheScreenshot(out); err != nil {
		return "", err
	}
	return out, nil
}

func prepareXiaoheiheMobileScreenshotPage(ctx context.Context, meta mediaMeta) error {
	if err := chromedp.Evaluate(xiaoheiheMobileScreenshotPrepareScript, nil).Do(ctx); err != nil {
		return fmt.Errorf("prepare mobile page: %w", err)
	}
	imageScript, err := xiaoheiheMobileScreenshotImageSourcesScript(meta.ImageURLs)
	if err != nil {
		return fmt.Errorf("encode mobile images: %w", err)
	}
	if err := chromedp.Evaluate(imageScript, nil).Do(ctx); err != nil {
		return fmt.Errorf("set mobile images: %w", err)
	}
	deadline := time.Now().Add(xiaoheiheMobileImageWait)
	for {
		var pending int
		if err := chromedp.Evaluate(xiaoheiheMobileScreenshotPendingImagesScript, &pending).Do(ctx); err != nil {
			return fmt.Errorf("inspect mobile images: %w", err)
		}
		if pending == 0 || !time.Now().Before(deadline) {
			break
		}
		if err := waitForXiaoheiheScreenshot(ctx, xiaoheiheMobileScrollPause); err != nil {
			return err
		}
	}
	if err := chromedp.Evaluate(xiaoheiheMobileScreenshotPrepareScript+";window.scrollTo({top:0,behavior:'instant'})", nil).Do(ctx); err != nil {
		return fmt.Errorf("finalize mobile page: %w", err)
	}
	return nil
}

const xiaoheiheMobileScreenshotPrepareScript = `(() => {
	const removableSelector = [
		'.heybox-share-header .download-btn',
		'.heybox-share-popover',
		'.hb-qrc',
		'.interactive-button-bar',
	].join(',');
	const removePromotions = () => document.querySelectorAll(removableSelector).forEach((element) => element.remove());
	removePromotions();
	document.querySelectorAll('img').forEach((image) => {
		image.loading = 'eager';
		image.decoding = 'sync';
	});
})()`

const xiaoheiheMobileScreenshotPendingImagesScript = `(() => {
	const captureBottom = Math.min(document.documentElement.scrollHeight, 10000);
	return Array.from(document.images).filter((image) => {
		const rect = image.getBoundingClientRect();
		const top = rect.top + window.scrollY;
		return rect.height > 0 && top < captureBottom && image.currentSrc && (!image.complete || image.naturalWidth === 0);
	}).length;
})()`

func xiaoheiheMobileScreenshotImageSourcesScript(groups [][]string) (string, error) {
	sources := make([]string, 0, len(groups))
	for _, group := range groups {
		for _, candidate := range group {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" {
				sources = append(sources, candidate)
				break
			}
		}
	}
	encoded, err := json.Marshal(sources)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`(() => {
		const sources = %s;
		const captureBottom = Math.min(document.documentElement.scrollHeight, 10000);
		Array.from(document.querySelectorAll('.bbs-link-article-content img.img-item')).forEach((image, index) => {
			const rect = image.getBoundingClientRect();
			const top = rect.top + window.scrollY;
			if (sources[index] && rect.height > 0 && top < captureBottom) image.src = sources[index];
		});
	})()`, encoded), nil
}

func waitForXiaoheiheScreenshot(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("prepare mobile page: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// newBrowserScreenshotContext creates an isolated CDP target. Other platforms can
// use it to render through the same configured browser without owning a Chromium binary.
func newBrowserScreenshotContext(ctx context.Context, endpoint string) (context.Context, context.CancelFunc, error) {
	if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
		wsURL, err := browserCDPWebSocketURL(ctx, endpoint)
		if err != nil {
			return nil, nil, err
		}
		allocatorCtx, stopAllocator := chromedp.NewRemoteAllocator(ctx, wsURL)
		browserCtx, stopBrowser := chromedp.NewContext(allocatorCtx)
		return browserCtx, func() {
			stopBrowser()
			stopAllocator()
		}, nil
	}

	browser, err := xiaoheiheBrowserPath()
	if err != nil {
		return nil, nil, err
	}
	profile, err := filepath.Abs(filepath.Join(cacheDir, "browser-screenshot-profile"))
	if err != nil {
		return nil, nil, fmt.Errorf("resolve browser profile path: %w", err)
	}
	if err := os.MkdirAll(profile, 0755); err != nil {
		return nil, nil, fmt.Errorf("create browser profile: %w", err)
	}
	allocatorOptions := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	allocatorOptions = append(allocatorOptions,
		chromedp.ExecPath(browser),
		chromedp.UserDataDir(profile),
		chromedp.WindowSize(xiaoheiheMobileWidth, xiaoheiheMobileViewportHeight),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("hide-scrollbars", true),
	)
	allocatorCtx, stopAllocator := chromedp.NewExecAllocator(ctx, allocatorOptions...)
	browserCtx, stopBrowser := chromedp.NewContext(allocatorCtx)
	return browserCtx, func() {
		stopBrowser()
		stopAllocator()
	}, nil
}

func browserCDPWebSocketURL(ctx context.Context, endpoint string) (string, error) {
	debugURL, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || debugURL.Host == "" {
		return "", fmt.Errorf("invalid browser CDP address: %q", endpoint)
	}
	if debugURL.Scheme == "ws" || debugURL.Scheme == "wss" {
		return debugURL.String(), nil
	}
	if debugURL.Scheme != "http" && debugURL.Scheme != "https" {
		return "", fmt.Errorf("browser CDP address must use http, https, ws, or wss")
	}
	debugURL.Path = strings.TrimRight(debugURL.Path, "/") + "/json/version"
	debugURL.RawQuery = ""
	debugURL.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, debugURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create browser version request: %w", err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("connect remote browser: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("remote browser version endpoint returned %s", resp.Status)
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
		return "", fmt.Errorf("decode remote browser version: %w", err)
	}
	wsURL, err := url.Parse(version.WebSocketDebuggerURL)
	if err != nil || wsURL.Host == "" || (wsURL.Scheme != "ws" && wsURL.Scheme != "wss") {
		return "", fmt.Errorf("remote browser returned an invalid WebSocket debugger URL")
	}
	wsURL.Host = debugURL.Host
	if debugURL.Scheme == "https" {
		wsURL.Scheme = "wss"
	}
	return wsURL.String(), nil
}

func validateXiaoheiheScreenshot(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open mobile screenshot: %w", err)
	}
	defer file.Close()
	config, err := png.DecodeConfig(file)
	if err != nil {
		return fmt.Errorf("decode mobile screenshot: %w", err)
	}
	if config.Width < xiaoheiheMobileWidth || config.Height < 400 {
		return fmt.Errorf("mobile screenshot dimensions are invalid: %dx%d", config.Width, config.Height)
	}
	return nil
}

func xiaoheiheBrowserPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv(xiaoheiheBrowserPathEnv)); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("configured browser does not exist: %s", configured)
	}
	candidates := []string{"chromium-browser", "chromium", "google-chrome", "google-chrome-stable"}
	if runtime.GOOS == "windows" {
		candidates = append([]string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}, candidates...)
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, string(filepath.Separator)) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("Chromium is required; set %s to its executable path", xiaoheiheBrowserPathEnv)
}
