// Package browserbroker executes immutable, read-only Browser Plans in an
// isolated Chrome process. It is an application-side data-plane adapter; it
// owns no Recruiting Work, scheduling, or result-acceptance authority.
package browserbroker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/temoto/robotstxt"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
)

type Runner struct {
	chromePath      string
	resolver        *net.Resolver
	robotsClient    *http.Client
	allowPrivate    bool
	profileResolver ProfileResolver
	requireProfile  bool
}

type brokerFailure struct {
	class string
	cause error
}

func (e *brokerFailure) Error() string               { return e.cause.Error() }
func (e *brokerFailure) Unwrap() error               { return e.cause }
func (e *brokerFailure) BrowserFailureClass() string { return e.class }

func policyFailure(cause error) error {
	return &brokerFailure{class: "effect_policy_violated", cause: cause}
}

type policyState struct {
	mu                   sync.Mutex
	initialOrigin        string
	methods              map[string]struct{}
	blockedMethods       map[string]struct{}
	documentNavigations  int
	blockedWriteRequests int
	crossOriginDocuments int
	formSubmissions      int
	downloads            int
	popups               int
	firstViolation       error
}

type requestTracker struct {
	mu   sync.Mutex
	wg   sync.WaitGroup
	open bool
}

func newRequestTracker() *requestTracker { return &requestTracker{open: true} }

func (t *requestTracker) start(run func()) {
	t.mu.Lock()
	if !t.open {
		t.mu.Unlock()
		return
	}
	t.wg.Add(1)
	t.mu.Unlock()
	go func() {
		defer t.wg.Done()
		run()
	}()
}

func (t *requestTracker) closeAndWait() {
	t.mu.Lock()
	t.open = false
	t.mu.Unlock()
	t.wg.Wait()
}

func New(chromePath string) (*Runner, error) {
	return newRunner(chromePath, nil, false)
}

func NewProfiled(chromePath string, resolver ProfileResolver) (*Runner, error) {
	if resolver == nil {
		return nil, errors.New("Profile Browser runner requires a local Profile resolver")
	}
	return newRunner(chromePath, resolver, true)
}

func newRunner(chromePath string, profileResolver ProfileResolver, requireProfile bool) (*Runner, error) {
	chromePath = strings.TrimSpace(chromePath)
	if chromePath == "" {
		return nil, errors.New("Chrome executable path is required")
	}
	info, err := os.Stat(chromePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("Chrome executable must be an executable regular file")
	}
	return &Runner{chromePath: chromePath, resolver: net.DefaultResolver,
		profileResolver: profileResolver, requireProfile: requireProfile}, nil
}

func (r *Runner) Run(ctx context.Context, request browserdriver.SessionRequest) (browserdriver.SessionResult, error) {
	if ctx == nil || r == nil || strings.TrimSpace(r.chromePath) == "" {
		return browserdriver.SessionResult{}, errors.New("browser runner is not configured")
	}
	profiled := request.ProfileRef != ""
	if profiled != (request.ProfileVersion != 0) || (profiled && r.profileResolver == nil) || (!profiled && r.requireProfile) {
		return browserdriver.SessionResult{}, policyFailure(errors.New("browser Profile reference, version, and local resolver are inconsistent"))
	}
	if err := request.Plan.Validate(); err != nil {
		return browserdriver.SessionResult{}, err
	}
	planHash, _ := request.Plan.ContentHash()
	if request.PlanHash != planHash || request.AttemptID == "" || strings.TrimSpace(request.UserAgent) == "" ||
		request.TimeoutMS < 100 || request.TimeoutMS > 60_000 ||
		!request.SameOriginDocs || !request.BlockDownloads || !request.BlockPopups ||
		len(request.AllowedMethods) != 3 || request.AllowedMethods[0] != http.MethodGet || request.AllowedMethods[1] != http.MethodHead ||
		request.AllowedMethods[2] != http.MethodOptions {
		return browserdriver.SessionResult{}, policyFailure(errors.New("browser session request is incomplete or unsafe"))
	}
	if err := request.Policy.Validate(); err != nil {
		return browserdriver.SessionResult{}, policyFailure(err)
	}
	endpoint, err := r.publicURL(ctx, request.EndpointURL)
	if err != nil {
		return browserdriver.SessionResult{}, err
	}
	var profileLease *ProfileLease
	if profiled {
		profileLease, err = r.profileResolver.Resolve(ctx, request.ProfileRef, request.ProfileVersion, endpoint.String())
		if err != nil {
			return browserdriver.SessionResult{}, &brokerFailure{class: "auth_expired", cause: err}
		}
		defer profileLease.Release()
	}
	robotsAllowed, err := r.robotsAllowed(ctx, endpoint, request.UserAgent)
	if err != nil || !robotsAllowed {
		if err == nil {
			err = errors.New("robots policy disallows browser endpoint")
		}
		return browserdriver.SessionResult{}, &brokerFailure{class: "robots_disallowed", cause: err}
	}

	runCtx, cancelRun := context.WithTimeout(ctx, time.Duration(request.TimeoutMS)*time.Millisecond)
	defer cancelRun()
	profileDir, profileDirectory := "", ""
	if profileLease == nil {
		profileDir, err = os.MkdirTemp("", "atoll-recruiting-browser-")
		if err != nil {
			return browserdriver.SessionResult{}, fmt.Errorf("create isolated Chrome profile: %w", err)
		}
		defer os.RemoveAll(profileDir)
	} else {
		profileDir, profileDirectory = profileLease.UserDataDir, profileLease.ProfileDirectory
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(r.chromePath), chromedp.UserDataDir(profileDir), chromedp.UserAgent(request.UserAgent),
		chromedp.WindowSize(1280, 900), chromedp.Flag("disable-extensions", true), chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-component-update", true), chromedp.Flag("no-proxy-server", true))
	if profileDirectory != "" {
		opts = append(opts, chromedp.Flag("profile-directory", profileDirectory))
	}
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(runCtx, opts...)
	defer cancelAllocator()
	tabCtx, cancelTab := chromedp.NewContext(allocatorCtx)
	defer cancelTab()

	state := &policyState{initialOrigin: origin(endpoint), methods: map[string]struct{}{}, blockedMethods: map[string]struct{}{}}
	requestTasks := newRequestTracker()
	chromedp.ListenTarget(tabCtx, func(event any) {
		switch value := event.(type) {
		case *fetch.EventRequestPaused:
			requestTasks.start(func() {
				allowed := state.inspectRequest(runCtx, r, value)
				_ = chromedp.Run(tabCtx, chromedp.ActionFunc(func(commandCtx context.Context) error {
					if allowed {
						return fetch.ContinueRequest(value.RequestID).Do(commandCtx)
					}
					return fetch.FailRequest(value.RequestID, network.ErrorReasonBlockedByClient).Do(commandCtx)
				}))
			})
		case *page.EventWindowOpen:
			state.mu.Lock()
			state.popups++
			state.setViolation(errors.New("page attempted to open a popup"))
			state.mu.Unlock()
		}
	})
	chromedp.ListenBrowser(tabCtx, func(event any) {
		if _, ok := event.(*browser.EventDownloadWillBegin); ok {
			state.mu.Lock()
			state.downloads++
			state.setViolation(errors.New("page attempted a download"))
			state.mu.Unlock()
		}
	})

	preventEffects := `(()=>{globalThis.__atollBlockedDownloads=0;globalThis.__atollBlockedPopups=0;const stop=e=>{e.preventDefault();e.stopImmediatePropagation()};document.addEventListener('submit',stop,true);document.addEventListener('click',e=>{const a=e.target&&e.target.closest&&e.target.closest('a[download]');if(a){globalThis.__atollBlockedDownloads++;stop(e)}},true);window.open=()=>{globalThis.__atollBlockedPopups++;return null}})()`
	setupErr := chromedp.Run(tabCtx,
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{
			{URLPattern: "*", RequestStage: fetch.RequestStageRequest},
			{URLPattern: "*", RequestStage: fetch.RequestStageResponse},
		}),
		network.SetExtraHTTPHeaders(network.Headers{"Accept-Language": request.AcceptLanguage}),
		browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorDeny).WithEventsEnabled(true),
		chromedp.ActionFunc(func(commandCtx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(preventEffects).Do(commandCtx)
			return err
		}),
	)
	if setupErr != nil {
		return browserdriver.SessionResult{}, fmt.Errorf("prepare Chrome effect policy: %w", setupErr)
	}
	navigationErr := chromedp.Run(tabCtx, chromedp.Navigate(endpoint.String()))
	if navigationErr == nil {
		navigationErr = detectFailureSignal(tabCtx, request.Plan)
	}
	if navigationErr == nil {
		for _, action := range request.Plan.Actions {
			if err := runAction(tabCtx, state, r, endpoint, action); err != nil {
				var classified browserdriver.ClassifiedBrokerError
				if action.Kind == browserdriver.ActionWaitSelector && !errors.As(err, &classified) {
					err = &brokerFailure{class: "parse_error", cause: err}
				}
				navigationErr = err
				break
			}
			if err := detectFailureSignal(tabCtx, request.Plan); err != nil {
				navigationErr = err
				break
			}
		}
	}

	var finalURL, contentType, dom string
	var blockedDownloads, blockedPopups int
	captureActions := []chromedp.Action{chromedp.Location(&finalURL),
		chromedp.Evaluate(`document.contentType || "text/html"`, &contentType),
		chromedp.Evaluate(`Number(globalThis.__atollBlockedDownloads||0)`, &blockedDownloads),
		chromedp.Evaluate(`Number(globalThis.__atollBlockedPopups||0)`, &blockedPopups)}
	if profiled {
		captureActions = append(captureActions, chromedp.Evaluate(profileSanitizedDOM, &dom))
	} else {
		captureActions = append(captureActions, chromedp.OuterHTML("html", &dom, chromedp.ByQuery))
	}
	captureErr := chromedp.Run(tabCtx, captureActions...)
	// Wait for Chrome to release its profile files before releasing the local
	// Profile lease; a plain context cancel can return while Chrome still owns
	// the directory and permit two processes to race over the same credentials.
	_ = chromedp.Cancel(tabCtx)
	cancelTab()
	requestTasks.closeAndWait()
	state.recordBlockedEffects(blockedDownloads, blockedPopups)
	attestation, violation := state.attestation(request.Policy.TermsPolicyVersion, robotsAllowed, profiled)
	if int64(len(dom)) > request.Plan.MaxDOMBytes {
		dom = dom[:request.Plan.MaxDOMBytes+1]
	}
	result := browserdriver.SessionResult{FinalURL: finalURL, ContentType: contentType, DOM: []byte(dom), Attestation: attestation}
	if violation != nil {
		return result, policyFailure(violation)
	}
	if navigationErr != nil {
		return result, fmt.Errorf("execute browser navigation: %w", navigationErr)
	}
	if captureErr != nil {
		return result, fmt.Errorf("capture browser DOM: %w", captureErr)
	}
	return result, nil
}

func detectFailureSignal(ctx context.Context, plan browserdriver.Plan) error {
	// Captcha wins if a page exposes both signals: it describes the immediate
	// intervention required more precisely than a generic signed-out marker.
	for _, group := range []struct {
		class     string
		selectors []string
	}{
		{class: "captcha", selectors: plan.CaptchaSelectors},
		{class: "auth_expired", selectors: plan.AuthExpiredSelectors},
	} {
		for _, selector := range group.selectors {
			var found bool
			expression := `document.querySelector(` + strconv.Quote(selector) + `)!==null`
			if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &found)); err != nil {
				return fmt.Errorf("inspect browser %s signal: %w", group.class, err)
			}
			if found {
				return &brokerFailure{class: group.class, cause: fmt.Errorf("browser page matched declared %s signal", group.class)}
			}
		}
	}
	return nil
}

const profileSanitizedDOM = `(()=>{
const root=document.documentElement.cloneNode(true);
root.querySelectorAll('script,style,form,input,textarea,select,button,iframe,object,embed').forEach(node=>node.remove());
for(const element of [root,...root.querySelectorAll('*')]){
 for(const attribute of [...element.attributes]){
  const name=attribute.name.toLowerCase();
  if(name==='style'||name==='value'||name==='checked'||name==='selected'||name.startsWith('on')||
    /(token|secret|password|session|authorization|signature|api.?key)/i.test(name)){element.removeAttribute(attribute.name);continue}
  if(name==='href'||name==='src'){
   try{const parsed=new URL(attribute.value,location.href);parsed.username='';parsed.password='';parsed.hash='';
    for(const key of [...parsed.searchParams.keys()])if(/(token|secret|password|session|authorization|signature|api.?key)/i.test(key))parsed.searchParams.delete(key);
    element.setAttribute(attribute.name,parsed.href)
   }catch(_){element.removeAttribute(attribute.name)}
  }
 }
}
return root.outerHTML
})()`

func runAction(ctx context.Context, state *policyState, runner *Runner, initial *url.URL,
	action browserdriver.Action) error {
	switch action.Kind {
	case browserdriver.ActionWaitSelector:
		timeout := time.Duration(action.TimeoutMS) * time.Millisecond
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return chromedp.Run(waitCtx, chromedp.WaitReady(action.Selector, chromedp.ByQuery))
	case browserdriver.ActionScrollPage:
		for count := 0; count < action.MaxRepeats; count++ {
			if err := chromedp.Run(ctx, chromedp.Evaluate(`window.scrollTo(0, document.documentElement.scrollHeight)`, nil),
				chromedp.Sleep(100*time.Millisecond)); err != nil {
				return err
			}
		}
		return nil
	case browserdriver.ActionFollowLink:
		selector := strconv.Quote(action.Selector)
		var target string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`(()=>{const node=document.querySelector(`+selector+`);return node&&node.href||""})()`, &target)); err != nil {
			return err
		}
		next, err := runner.publicURL(ctx, target)
		if err != nil || origin(next) != origin(initial) {
			return policyFailure(errors.New("follow_link target is missing, non-public, or cross-origin"))
		}
		return chromedp.Run(ctx, chromedp.Navigate(next.String()))
	default:
		return fmt.Errorf("unsupported browser action %q", action.Kind)
	}
}

func (s *policyState) inspectRequest(ctx context.Context, runner *Runner, event *fetch.EventRequestPaused) bool {
	if event.ResponseStatusCode != 0 || event.ResponseErrorReason != "" {
		for _, header := range event.ResponseHeaders {
			if strings.EqualFold(strings.TrimSpace(header.Name), "Content-Disposition") &&
				strings.Contains(strings.ToLower(header.Value), "attachment") {
				s.mu.Lock()
				s.downloads++
				s.setViolation(errors.New("page attempted a download"))
				s.mu.Unlock()
				return false
			}
		}
		return true
	}
	method := strings.ToUpper(strings.TrimSpace(event.Request.Method))
	// OPTIONS is a read-only capability/preflight probe used by modern sites.
	// It may proceed. Methods capable of application writes are blocked before
	// reaching the origin and retained as explicit blocked-effect evidence;
	// a document-level write is additionally a terminal navigation violation.
	allowed := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
	if !allowed {
		s.mu.Lock()
		s.blockedWriteRequests++
		s.blockedMethods[method] = struct{}{}
		if event.ResourceType == network.ResourceTypeDocument {
			s.formSubmissions++
			s.setViolation(fmt.Errorf("browser document request used unsafe method %s", method))
		}
		s.mu.Unlock()
		return false
	}
	s.mu.Lock()
	s.methods[method] = struct{}{}
	s.mu.Unlock()
	parsed, err := runner.publicURL(ctx, event.Request.URL)
	if err != nil {
		s.mu.Lock()
		s.setViolation(fmt.Errorf("browser request targeted a non-public URL"))
		s.mu.Unlock()
		return false
	}
	if event.ResourceType == network.ResourceTypeDocument {
		s.mu.Lock()
		s.documentNavigations++
		if origin(parsed) != s.initialOrigin {
			s.crossOriginDocuments++
			s.setViolation(errors.New("browser document navigation crossed origin"))
			allowed = false
		}
		s.mu.Unlock()
	}
	return allowed
}

func (s *policyState) setViolation(err error) {
	if s.firstViolation == nil {
		s.firstViolation = err
	}
}

func (s *policyState) recordBlockedEffects(downloads, popups int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if downloads > 0 {
		s.downloads += downloads
		s.setViolation(errors.New("page attempted a download"))
	}
	if popups > 0 {
		s.popups += popups
		s.setViolation(errors.New("page attempted to open a popup"))
	}
}

func (s *policyState) attestation(termsVersion uint64, robotsAllowed, profileLeaseAuthorized bool) (browserdriver.Attestation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	methods := make([]string, 0, len(s.methods))
	for method := range s.methods {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	blockedMethods := make([]string, 0, len(s.blockedMethods))
	for method := range s.blockedMethods {
		blockedMethods = append(blockedMethods, method)
	}
	sort.Strings(blockedMethods)
	return browserdriver.Attestation{DocumentNavigations: s.documentNavigations, ObservedMethods: methods,
		BlockedMethods: blockedMethods, AllowedWriteRequests: 0, BlockedWriteRequests: s.blockedWriteRequests,
		CrossOriginDocumentNavigations: s.crossOriginDocuments,
		FormSubmissions:                s.formSubmissions, Downloads: s.downloads, Popups: s.popups, PublicEndpoint: s.firstViolation == nil,
		RobotsAllowed: robotsAllowed, TermsPolicyVersion: termsVersion, ProfileLeaseAuthorized: profileLeaseAuthorized}, s.firstViolation
}

func (r *Runner) publicURL(ctx context.Context, raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, &brokerFailure{class: "endpoint_rejected", cause: errors.New("browser target must be an absolute HTTP(S) URL without credentials or fragment")}
	}
	if r.allowPrivate {
		return parsed, nil
	}
	resolver := r.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("browser target hostname did not resolve")
	}
	for _, address := range addresses {
		if !publicAddress(address) {
			return nil, &brokerFailure{class: "endpoint_rejected", cause: errors.New("browser target resolved outside the public network")}
		}
	}
	return parsed, nil
}

func publicAddress(address netip.Addr) bool {
	return address.IsValid() && !address.IsPrivate() && !address.IsLoopback() && !address.IsLinkLocalUnicast() &&
		!address.IsLinkLocalMulticast() && !address.IsMulticast() && !address.IsUnspecified()
}

func origin(value *url.URL) string {
	return strings.ToLower(value.Scheme + "://" + value.Host)
}

func (r *Runner) robotsAllowed(ctx context.Context, endpoint *url.URL, userAgent string) (bool, error) {
	if r.allowPrivate && r.robotsClient == nil {
		return true, nil
	}
	client := r.robotsClient
	if client == nil {
		transport := &http.Transport{Proxy: nil, DialContext: r.secureDialContext,
			TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
		client = &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > 3 || origin(request.URL) != origin(endpoint) {
				return errors.New("robots redirect crossed origin or limit")
			}
			if _, err := r.publicURL(request.Context(), request.URL.String()); err != nil {
				return err
			}
			return nil
		}}
	}
	robotsURL := *endpoint
	robotsURL.Path, robotsURL.RawQuery, robotsURL.Fragment = "/robots.txt", "", ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL.String(), nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		return true, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("robots endpoint returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return false, err
	}
	if len(body) > 1<<20 {
		return false, errors.New("robots policy exceeded one MiB")
	}
	data, err := robotstxt.FromBytes(body)
	if err != nil {
		return false, err
	}
	return data.TestAgent(endpoint.Path, userAgent), nil
}

func (r *Runner) secureDialContext(ctx context.Context, networkName, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	resolver := r.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("outbound hostname did not resolve")
	}
	for _, candidate := range addresses {
		if !r.allowPrivate && !publicAddress(candidate) {
			return nil, errors.New("outbound hostname resolved outside the public network")
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, networkName, net.JoinHostPort(addresses[0].String(), port))
}
