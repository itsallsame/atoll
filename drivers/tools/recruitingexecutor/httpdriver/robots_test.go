package httpdriver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func robotsPolicy() RobotsPolicy {
	return RobotsPolicy{Timeout: time.Second, MaxBytes: 1024, CacheTTL: time.Hour}
}

func TestRobotsCheckerCollapsesConcurrentCacheMissesPerOrigin(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		time.Sleep(20 * time.Millisecond)
		_, _ = response.Write([]byte("User-agent: *\nAllow: /\n"))
	}))
	defer server.Close()
	checker, _ := newRobotsTxtChecker(robotsPolicy(), true)
	target, _ := url.Parse(server.URL + "/jobs")
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := checker.Allowed(context.Background(), target, "Atoll-Recruiting-Test/1"); err != nil {
				t.Errorf("concurrent robots check: %v", err)
			}
		}()
	}
	group.Wait()
	if requests.Load() != 1 {
		t.Fatalf("concurrent cache miss made %d origin requests", requests.Load())
	}
}

func TestRobotsCheckerParsesCachesAndCarriesCrawlDelay(t *testing.T) {
	var robotsRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/robots.txt" {
			t.Fatalf("unexpected website request during direct robots check: %s", request.URL.Path)
		}
		robotsRequests.Add(1)
		_, _ = response.Write([]byte("User-agent: Atoll-Recruiting-Test\nDisallow: /private\nAllow: /public\nCrawl-delay: 1\n"))
	}))
	defer server.Close()
	checker, err := newRobotsTxtChecker(robotsPolicy(), true)
	if err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string]bool{"/public/jobs": true, "/private/jobs": false} {
		target, _ := url.Parse(server.URL + path)
		evidence, err := checker.Allowed(context.Background(), target, "Atoll-Recruiting-Test/1")
		if err != nil || evidence.Allowed != expected || evidence.CrawlDelayMS != 1000 || !strings.HasPrefix(evidence.ContentHash, "sha256:") {
			t.Fatalf("path=%s evidence=%+v err=%v", path, evidence, err)
		}
	}
	if robotsRequests.Load() != 1 {
		t.Fatalf("robots policy was not cached: requests=%d", robotsRequests.Load())
	}
}

func TestDriverWithRobotsCheckerNeverFetchesDisallowedPath(t *testing.T) {
	var websiteRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/robots.txt" {
			_, _ = response.Write([]byte("User-agent: *\nDisallow: /blocked\n"))
			return
		}
		websiteRequests.Add(1)
		_, _ = response.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	checker, _ := newRobotsTxtChecker(robotsPolicy(), true)
	driver, _ := newDriver(testPolicy(), checker, true)
	if _, err := driver.Fetch(context.Background(), testSpec(), testInput(server.URL+"/blocked/jobs"), compliance); err == nil {
		t.Fatal("robots-disallowed website request succeeded")
	}
	if websiteRequests.Load() != 0 {
		t.Fatalf("driver contacted disallowed path %d times", websiteRequests.Load())
	}
}

func TestRobotsCheckerFailsClosedOnProtectedOrOversizedPolicy(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"protected": func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusForbidden) },
		"oversized": func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(strings.Repeat("x", 2048)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			checker, _ := newRobotsTxtChecker(robotsPolicy(), true)
			target, _ := url.Parse(server.URL + "/jobs")
			if _, err := checker.Allowed(context.Background(), target, "Atoll-Recruiting-Test/1"); err == nil {
				t.Fatal("unavailable robots policy was treated as allowed")
			}
		})
	}
}
