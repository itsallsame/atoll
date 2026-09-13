// Package httpdriver performs bounded, read-only public HTTP effects for a
// validated recruiting Recipe. It owns transport safety, not business state.
package httpdriver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type RobotsEvidence struct {
	PolicyURL    string `json:"policy_url"`
	ContentHash  string `json:"content_hash"`
	Allowed      bool   `json:"allowed"`
	CheckedAt    string `json:"checked_at"`
	CrawlDelayMS int64  `json:"crawl_delay_ms,omitempty"`
}

func (e RobotsEvidence) Validate() error {
	policyURL, err := url.Parse(e.PolicyURL)
	if err != nil || (policyURL.Scheme != "http" && policyURL.Scheme != "https") || policyURL.Host == "" ||
		!strings.HasPrefix(e.ContentHash, "sha256:") {
		return fmt.Errorf("robots evidence requires policy URL and content hash")
	}
	if _, err := time.Parse(time.RFC3339, e.CheckedAt); err != nil || e.CrawlDelayMS < 0 || e.CrawlDelayMS > int64(time.Hour/time.Millisecond) {
		return fmt.Errorf("robots evidence checked_at must be RFC3339")
	}
	return nil
}

type RobotsChecker interface {
	Allowed(context.Context, *url.URL, string) (RobotsEvidence, error)
}

type ComplianceEvidence struct {
	TermsPolicyVersion uint64 `json:"terms_policy_version"`
	TermsReviewedAt    string `json:"terms_reviewed_at"`
}

func (e ComplianceEvidence) Validate() error {
	if e.TermsPolicyVersion == 0 {
		return fmt.Errorf("terms policy version is required")
	}
	if _, err := time.Parse(time.RFC3339, e.TermsReviewedAt); err != nil {
		return fmt.Errorf("terms review time must be RFC3339")
	}
	return nil
}

type Policy struct {
	MaxConcurrency    int
	MinOriginInterval time.Duration
	CircuitThreshold  int
	CircuitCooldown   time.Duration
}

func (p Policy) validate() error {
	if p.MaxConcurrency < 1 || p.MaxConcurrency > 1000 || p.MinOriginInterval < 0 || p.MinOriginInterval > time.Hour || p.CircuitThreshold < 1 || p.CircuitThreshold > 100 ||
		p.CircuitCooldown < time.Second || p.CircuitCooldown > 24*time.Hour {
		return fmt.Errorf("invalid HTTP driver policy")
	}
	return nil
}

type Result struct {
	StatusCode  int                `json:"status_code"`
	ContentType string             `json:"content_type"`
	FinalURL    string             `json:"final_url"`
	Body        []byte             `json:"-"`
	ContentHash string             `json:"content_hash"`
	Robots      RobotsEvidence     `json:"robots"`
	Compliance  ComplianceEvidence `json:"compliance"`
}

type FetchError struct {
	Class      string
	Retryable  bool
	StatusCode int
	Cause      error
}

func (e *FetchError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("http driver %s: %v", e.Class, e.Cause)
	}
	return fmt.Sprintf("http driver %s (status=%d)", e.Class, e.StatusCode)
}

func (e *FetchError) Unwrap() error { return e.Cause }

type circuitState struct {
	failures  int
	openUntil time.Time
}

type Driver struct {
	policy     Policy
	robots     RobotsChecker
	client     *http.Client
	now        func() time.Time
	sem        chan struct{}
	mu         sync.Mutex
	nextOrigin map[string]time.Time
	circuits   map[string]circuitState
}

func New(policy Policy, robots RobotsChecker) (*Driver, error) {
	return newDriver(policy, robots, false)
}

func newDriver(policy Policy, robots RobotsChecker, allowPrivate bool) (*Driver, error) {
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if robots == nil {
		return nil, fmt.Errorf("robots checker is required")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext:           secureDialContext(dialer, net.DefaultResolver, allowPrivate),
		ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 90 * time.Second,
	}
	driver := &Driver{
		policy: policy, robots: robots, now: time.Now, sem: make(chan struct{}, policy.MaxConcurrency),
		nextOrigin: map[string]time.Time{}, circuits: map[string]circuitState{},
	}
	driver.client = &http.Client{Transport: transport, CheckRedirect: driver.checkRedirect}
	return driver, nil
}

func (d *Driver) Fetch(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput, compliance ComplianceEvidence) (Result, error) {
	if err := spec.Validate(); err != nil {
		return Result{}, err
	}
	if err := input.Validate(); err != nil {
		return Result{}, err
	}
	if err := compliance.Validate(); err != nil {
		return Result{}, err
	}
	if spec.Transport != recipeabi.TransportHTTPJSON && spec.Transport != recipeabi.TransportHTTPHTML {
		return Result{}, fmt.Errorf("HTTP driver cannot run transport %q", spec.Transport)
	}
	endpoint, _ := url.Parse(input.Endpoint.URL)
	origin := normalizedOrigin(endpoint)
	if err := d.circuitAllows(origin); err != nil {
		return Result{}, err
	}
	if err := d.acquire(ctx, origin); err != nil {
		return Result{}, &FetchError{Class: "transport_timeout", Retryable: true, Cause: err}
	}
	defer func() { <-d.sem }()

	robots, err := d.robots.Allowed(ctx, endpoint, spec.Request.UserAgent)
	if err != nil {
		return Result{}, &FetchError{Class: "robots_disallowed", Retryable: true, Cause: err}
	}
	if err := robots.Validate(); err != nil {
		return Result{}, &FetchError{Class: "robots_disallowed", Retryable: false, Cause: err}
	}
	if !robots.Allowed {
		return Result{Robots: robots, Compliance: compliance}, &FetchError{Class: "robots_disallowed", Retryable: false}
	}
	d.extendOriginInterval(origin, time.Duration(robots.CrawlDelayMS)*time.Millisecond)
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(spec.Request.TimeoutMS)*time.Millisecond)
	defer cancel()
	requestCtx = context.WithValue(requestCtx, redirectLimitKey{}, spec.Request.MaxRedirects)
	method := strings.ToUpper(strings.TrimSpace(spec.Request.Method))
	var requestBody io.Reader
	if method == http.MethodPost {
		requestBody = bytes.NewReader(spec.Request.JSONBody)
	}
	request, err := http.NewRequestWithContext(requestCtx, method, endpoint.String(), requestBody)
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("User-Agent", spec.Request.UserAgent)
	for name, value := range spec.Request.Headers {
		request.Header.Set(name, value)
	}
	response, err := d.client.Do(request)
	if err != nil {
		class := "transport_timeout"
		retryable := true
		var redirectErr *redirectRejectedError
		var endpointErr *endpointRejectedError
		if errors.As(err, &redirectErr) {
			class = "redirect_rejected"
			retryable = false
		} else if errors.As(err, &endpointErr) {
			class = "endpoint_rejected"
			retryable = false
		}
		return Result{Robots: robots, Compliance: compliance}, &FetchError{Class: class, Retryable: retryable, Cause: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, spec.Request.MaxResponseBytes+1))
	tooLarge := int64(len(body)) > spec.Request.MaxResponseBytes
	if tooLarge {
		body = body[:spec.Request.MaxResponseBytes]
	}
	result := Result{StatusCode: response.StatusCode, ContentType: response.Header.Get("Content-Type"), FinalURL: response.Request.URL.String(),
		Body: body, Robots: robots, Compliance: compliance}
	sum := sha256.Sum256(body)
	result.ContentHash = "sha256:" + hex.EncodeToString(sum[:])
	if tooLarge {
		d.noteFailure(origin, response.StatusCode, response.Header.Get("Retry-After"))
		return result, &FetchError{Class: "response_too_large", Retryable: false, StatusCode: response.StatusCode}
	}
	if err != nil {
		return result, &FetchError{Class: "transport_timeout", Retryable: true, StatusCode: response.StatusCode, Cause: err}
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		d.noteSuccess(origin)
		return result, nil
	}
	d.noteFailure(origin, response.StatusCode, response.Header.Get("Retry-After"))
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return result, &FetchError{Class: "throttled", Retryable: true, StatusCode: response.StatusCode}
	case response.StatusCode == http.StatusForbidden:
		return result, &FetchError{Class: "forbidden", Retryable: false, StatusCode: response.StatusCode}
	case response.StatusCode >= 500:
		return result, &FetchError{Class: "upstream_5xx", Retryable: true, StatusCode: response.StatusCode}
	default:
		return result, &FetchError{Class: "unexpected_status", Retryable: false, StatusCode: response.StatusCode}
	}
}

func (d *Driver) acquire(ctx context.Context, origin string) error {
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	d.mu.Lock()
	now := d.now()
	waitUntil := d.nextOrigin[origin]
	if waitUntil.Before(now) {
		waitUntil = now
	}
	d.nextOrigin[origin] = waitUntil.Add(d.policy.MinOriginInterval)
	d.mu.Unlock()
	if delay := waitUntil.Sub(now); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			<-d.sem
			return ctx.Err()
		}
	}
	return nil
}

func (d *Driver) circuitAllows(origin string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.circuits[origin]
	if state.openUntil.After(d.now()) {
		return &FetchError{Class: "throttled", Retryable: true, Cause: fmt.Errorf("origin circuit open")}
	}
	return nil
}

func (d *Driver) noteSuccess(origin string) {
	d.mu.Lock()
	delete(d.circuits, origin)
	d.mu.Unlock()
}

func (d *Driver) extendOriginInterval(origin string, delay time.Duration) {
	if delay <= d.policy.MinOriginInterval {
		return
	}
	d.mu.Lock()
	candidate := d.now().Add(delay)
	if d.nextOrigin[origin].Before(candidate) {
		d.nextOrigin[origin] = candidate
	}
	d.mu.Unlock()
}

func (d *Driver) noteFailure(origin string, status int, retryAfter string) {
	if status != http.StatusTooManyRequests && status != http.StatusForbidden && status < 500 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.circuits[origin]
	state.failures++
	if state.failures >= d.policy.CircuitThreshold {
		state.openUntil = d.now().Add(d.policy.CircuitCooldown)
		if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 {
			candidate := d.now().Add(time.Duration(seconds) * time.Second)
			if candidate.After(state.openUntil) && candidate.Before(d.now().Add(24*time.Hour)) {
				state.openUntil = candidate
			}
		}
	}
	d.circuits[origin] = state
}

func (d *Driver) checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	maxRedirects := 0
	if limit, ok := via[0].Context().Value(redirectLimitKey{}).(int); ok {
		maxRedirects = limit
	}
	if len(via) > maxRedirects {
		return &redirectRejectedError{reason: "limit exceeded"}
	}
	first := via[0].URL
	if !strings.EqualFold(first.Host, request.URL.Host) || first.Scheme != request.URL.Scheme {
		return &redirectRejectedError{reason: "crossed origin or scheme"}
	}
	return nil
}

type redirectLimitKey struct{}

type redirectRejectedError struct{ reason string }

func (e *redirectRejectedError) Error() string { return "redirect rejected: " + e.reason }

type endpointRejectedError struct{ reason string }

func (e *endpointRejectedError) Error() string { return "endpoint rejected: " + e.reason }

func normalizedOrigin(target *url.URL) string {
	return strings.ToLower(target.Scheme + "://" + target.Host)
}

func secureDialContext(dialer *net.Dialer, resolver *net.Resolver, allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("resolve public endpoint: %w", err)
		}
		for _, address := range addresses {
			if !allowPrivate && !publicAddress(address) {
				return nil, &endpointRejectedError{reason: "resolved to a non-public address"}
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
}

func publicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || address.IsUnspecified() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsMulticast() {
		return false
	}
	for _, prefix := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32"} {
		network, _ := netip.ParsePrefix(prefix)
		if network.Contains(address) {
			return false
		}
	}
	return true
}
