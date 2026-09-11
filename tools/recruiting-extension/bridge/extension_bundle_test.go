package bridge

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// This opt-in test loads the actual Manifest V3 directory, opens its real
// popup, and clicks through pairing. Unit-testing background.js alone cannot
// prove Chrome accepted the manifest, service worker, permissions, popup, and
// runtime messaging as one bundle.
func TestRecruitingExtensionBundlePairsManagedProfileThroughPopup(t *testing.T) {
	chromePath := strings.TrimSpace(os.Getenv("RECRUITING_CHROME_BIN"))
	if chromePath == "" {
		t.Skip("set RECRUITING_CHROME_BIN to Chrome for Testing or Chromium")
	}
	if info, err := os.Stat(chromePath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("RECRUITING_CHROME_BIN is not an executable regular file: %v", err)
	}
	registry, profileToken, profileDirectory := testProfileRegistry(t, 1)
	service := &Service{Client: &submissionFake{source: readyBridgeSource(), sender: "human:operator:7"},
		Token: strings.Repeat("bundle-extension-token-", 2), ProfileRegistry: registry, Now: time.Now}
	handler, err := service.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	parsedServer, _ := url.Parse(server.URL)
	bridgeEndpoint := "ws://" + parsedServer.Host + "/capture"

	extensionDirectory, err := filepath.Abs("../extension")
	if err != nil {
		t.Fatal(err)
	}
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.ExecPath(chromePath), chromedp.UserDataDir(profileDirectory),
		chromedp.Flag("headless", "new"), chromedp.Flag("no-sandbox", true), chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-extensions", false), chromedp.Flag("disable-extensions-except", extensionDirectory),
		chromedp.Flag("load-extension", extensionDirectory))
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(context.Background(), options...)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	defer cancelBrowser()
	runCtx, cancelRun := context.WithTimeout(browserCtx, 20*time.Second)
	defer cancelRun()
	if err := chromedp.Run(runCtx); err != nil {
		t.Fatal(err)
	}

	extensionID := waitForRecruitingExtensionID(t, runCtx)
	popupCtx, cancelPopup := chromedp.NewContext(browserCtx)
	defer cancelPopup()
	popupURL := "chrome-extension://" + extensionID + "/popup.html"
	if err := chromedp.Run(popupCtx,
		chromedp.Navigate(popupURL),
		chromedp.WaitVisible("#pair", chromedp.ByQuery),
		chromedp.SetValue("#endpoint", bridgeEndpoint, chromedp.ByQuery),
		chromedp.SetValue("#token", service.Token, chromedp.ByQuery),
		chromedp.SetValue("#profile-id", "profile-1", chromedp.ByQuery),
		chromedp.SetValue("#profile-token", profileToken, chromedp.ByQuery),
		chromedp.Click("#pair", chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var phase string
		if err := chromedp.Run(popupCtx, chromedp.Text("#phase", &phase, chromedp.ByQuery)); err == nil && phase == "已连接" {
			service.profileMu.Lock()
			connection := service.profileConns["profile-1"]
			service.profileMu.Unlock()
			if connection == nil || connection.profileID != "profile-1" {
				t.Fatal("popup reported connected without a managed Profile connection at the Bridge")
			}
			blockedCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			lease, leaseErr := registry.Resolve(blockedCtx, "profile://recruiting/profile-1", 1,
				"https://jobs.example.test/private")
			cancel()
			if lease != nil {
				lease.Release()
			}
			if !errors.Is(leaseErr, context.DeadlineExceeded) {
				t.Fatalf("real extension connection did not exclude a daily Profile session: %v", leaseErr)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	var message string
	_ = chromedp.Run(popupCtx, chromedp.Text("#message", &message, chromedp.ByQuery))
	t.Fatalf("extension popup did not complete managed Profile pairing: %s", message)
}

func waitForRecruitingExtensionID(t *testing.T, ctx context.Context) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		targets, err := chromedp.Targets(ctx)
		if err == nil {
			for _, target := range targets {
				parsed, parseErr := url.Parse(target.URL)
				if parseErr == nil && target.Type == "service_worker" && parsed.Scheme == "chrome-extension" &&
					strings.HasSuffix(parsed.Path, "/background.js") {
					workerCtx, cancel := chromedp.NewContext(ctx, chromedp.WithTargetID(target.TargetID))
					var name string
					evaluateErr := chromedp.Run(workerCtx,
						chromedp.Evaluate(`chrome.runtime.getManifest().name`, &name))
					cancel()
					if evaluateErr == nil && name == "Atoll Recruiting Capture" {
						return parsed.Host
					}
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("Chrome did not load the recruiting extension service worker")
	return ""
}
