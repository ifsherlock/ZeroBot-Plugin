package mediaparser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestXiaoheiheMobileScreenshotEnabled(t *testing.T) {
	t.Setenv(xiaoheiheBrowserEnv, "")
	if xiaoheiheMobileScreenshotEnabled() {
		t.Fatal("empty setting should disable browser screenshot")
	}
	for _, value := range []string{"1", "true", "YES"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(xiaoheiheBrowserEnv, value)
			if !xiaoheiheMobileScreenshotEnabled() {
				t.Fatalf("setting %q should enable browser screenshot", value)
			}
		})
	}
}

func TestXiaoheiheMobileScreenshotViewport(t *testing.T) {
	if xiaoheiheMobileWidth != 430 || xiaoheiheMobileCaptureHeight != 10000 || xiaoheiheMobileDeviceScale != 2 {
		t.Fatalf("unexpected mobile viewport: %dx%d@%d", xiaoheiheMobileWidth, xiaoheiheMobileCaptureHeight, xiaoheiheMobileDeviceScale)
	}
}

func TestXiaoheiheBrowserWebSocketURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/browser-id"}`)
	}))
	defer server.Close()

	got, err := browserCDPWebSocketURL(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := "ws://" + strings.TrimPrefix(server.URL, "http://") + "/devtools/browser/browser-id"
	if got != want {
		t.Fatalf("WebSocket URL = %q, want %q", got, want)
	}
}

func TestXiaoheiheBrowserWebSocketURLUsesDirectWebSocket(t *testing.T) {
	const want = "ws://browser.internal:9222/devtools/browser/browser-id"
	got, err := browserCDPWebSocketURL(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("WebSocket URL = %q, want %q", got, want)
	}
}

func TestNormalizeConfigTrimsBrowserCDPURL(t *testing.T) {
	cfg := defaultConfig()
	cfg.BrowserCDPURL = "  http://browser-host:9222/  "
	normalizeConfig(&cfg)
	if cfg.BrowserCDPURL != "http://browser-host:9222/" {
		t.Fatalf("browser CDP URL = %q", cfg.BrowserCDPURL)
	}
}

func TestBrowserCDPURLIncludedInWebConfig(t *testing.T) {
	stateMu.Lock()
	old := currentConf
	currentConf = defaultConfig()
	currentConf.BrowserCDPURL = "http://browser-host:9222"
	stateMu.Unlock()
	t.Cleanup(func() {
		stateMu.Lock()
		currentConf = old
		stateMu.Unlock()
	})

	if got := configForWeb()["browser_cdp_url"]; got != "http://browser-host:9222" {
		t.Fatalf("browser_cdp_url = %#v", got)
	}
}
