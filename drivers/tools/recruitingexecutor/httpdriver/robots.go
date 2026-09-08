package httpdriver

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

type RobotsPolicy struct {
	Timeout  time.Duration
	MaxBytes int64
	CacheTTL time.Duration
}

func (p RobotsPolicy) validate() error {
	if p.Timeout < 100*time.Millisecond || p.Timeout > 30*time.Second || p.MaxBytes < 1 || p.MaxBytes > 1<<20 ||
		p.CacheTTL < time.Minute || p.CacheTTL > 24*time.Hour {
		return fmt.Errorf("invalid robots policy")
	}
	return nil
}

type robotsCacheEntry struct {
	data        *robotstxt.RobotsData
	policyURL   string
	contentHash string
	checkedAt   time.Time
	expiresAt   time.Time
}

type RobotsTxtChecker struct {
	policy RobotsPolicy
	client *http.Client
	now    func() time.Time
	mu     sync.Mutex
	cache  map[string]robotsCacheEntry
	locks  map[string]*sync.Mutex
}

func NewRobotsTxtChecker(policy RobotsPolicy) (*RobotsTxtChecker, error) {
	return newRobotsTxtChecker(policy, false)
}

func newRobotsTxtChecker(policy RobotsPolicy, allowPrivate bool) (*RobotsTxtChecker, error) {
	if err := policy.validate(); err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: policy.Timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: secureDialContext(dialer, net.DefaultResolver, allowPrivate), ResponseHeaderTimeout: policy.Timeout, IdleConnTimeout: 90 * time.Second}
	checker := &RobotsTxtChecker{policy: policy, now: time.Now, cache: map[string]robotsCacheEntry{}, locks: map[string]*sync.Mutex{}}
	checker.client = &http.Client{Transport: transport, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) > 1 || len(via) > 0 && (via[0].URL.Scheme != request.URL.Scheme || !strings.EqualFold(via[0].URL.Host, request.URL.Host)) {
			return &redirectRejectedError{reason: "robots redirect crossed origin or exceeded limit"}
		}
		return nil
	}}
	return checker, nil
}

func (c *RobotsTxtChecker) Allowed(ctx context.Context, target *url.URL, userAgent string) (RobotsEvidence, error) {
	if target == nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || strings.TrimSpace(userAgent) == "" {
		return RobotsEvidence{}, fmt.Errorf("robots check requires endpoint and user agent")
	}
	origin := normalizedOrigin(target)
	entry, err := c.entry(ctx, origin)
	if err != nil {
		return RobotsEvidence{}, err
	}
	path := target.RequestURI()
	if path == "" {
		path = "/"
	}
	group := entry.data.FindGroup(userAgent)
	allowed := group == nil || group.Test(path)
	var crawlDelay time.Duration
	if group != nil {
		crawlDelay = group.CrawlDelay
	}
	return RobotsEvidence{PolicyURL: entry.policyURL, ContentHash: entry.contentHash, Allowed: allowed,
		CheckedAt: entry.checkedAt.UTC().Format(time.RFC3339), CrawlDelayMS: crawlDelay.Milliseconds()}, nil
}

func (c *RobotsTxtChecker) entry(ctx context.Context, origin string) (robotsCacheEntry, error) {
	c.mu.Lock()
	entry, found := c.cache[origin]
	if found && entry.expiresAt.After(c.now()) {
		c.mu.Unlock()
		return entry, nil
	}
	c.mu.Unlock()
	lock := c.originLock(origin)
	lock.Lock()
	defer lock.Unlock()
	c.mu.Lock()
	entry, found = c.cache[origin]
	if found && entry.expiresAt.After(c.now()) {
		c.mu.Unlock()
		return entry, nil
	}
	c.mu.Unlock()
	policyURL := origin + "/robots.txt"
	requestCtx, cancel := context.WithTimeout(ctx, c.policy.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, policyURL, nil)
	if err != nil {
		return robotsCacheEntry{}, err
	}
	request.Header.Set("User-Agent", "Atoll-Recruiting-Robots/1")
	response, err := c.client.Do(request)
	if err != nil {
		return robotsCacheEntry{}, fmt.Errorf("fetch robots policy: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, c.policy.MaxBytes+1))
	if err != nil {
		return robotsCacheEntry{}, fmt.Errorf("read robots policy: %w", err)
	}
	if int64(len(body)) > c.policy.MaxBytes {
		return robotsCacheEntry{}, fmt.Errorf("robots policy exceeds byte limit")
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
		return robotsCacheEntry{}, fmt.Errorf("robots policy unavailable with status %d", response.StatusCode)
	}
	data, err := robotstxt.FromStatusAndBytes(response.StatusCode, body)
	if err != nil {
		return robotsCacheEntry{}, fmt.Errorf("parse robots policy: %w", err)
	}
	now := c.now().UTC()
	sum := sha256.Sum256(body)
	entry = robotsCacheEntry{data: data, policyURL: policyURL, contentHash: "sha256:" + hex.EncodeToString(sum[:]),
		checkedAt: now, expiresAt: now.Add(c.policy.CacheTTL)}
	c.mu.Lock()
	if existing, ok := c.cache[origin]; ok && existing.expiresAt.After(now) {
		entry = existing
	} else {
		c.cache[origin] = entry
	}
	c.mu.Unlock()
	return entry, nil
}

func (c *RobotsTxtChecker) originLock(origin string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	lock := c.locks[origin]
	if lock == nil {
		lock = &sync.Mutex{}
		c.locks[origin] = lock
	}
	return lock
}
