// Package browserbroker executes immutable, read-only Browser Plans in an
// isolated Chrome process. It is an application-side data-plane adapter; it
// owns no Recruiting Work, scheduling, or result-acceptance authority.
package browserbroker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
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
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
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
	mu                    sync.Mutex
	initialOrigin         string
	maxNavigations        int
	methods               map[string]struct{}
	blockedMethods        map[string]struct{}
	documentNavigations   int
	blockedWriteRequests  int
	crossOriginDocuments  int
	formSubmissions       int
	downloads             int
	popups                int
	firstViolation        error
	publicQueries         map[string]recipeabi.PublicQueryObservation
	publicQueryBytes      int
	pendingPublicQueries  map[string]recipeabi.PublicQueryObservation
	pendingQueryResponses map[string]publicQueryResponseMetadata
	publicQueryResponses  []browserdriver.PublicQueryResponse
	publicResponseBytes   int
	allowedPublicQueries  int
	currentActionSequence int
	pendingQueryActions   map[string]int
	responseSignal        chan struct{}
}

type publicQueryResponseMetadata struct {
	observation    recipeabi.PublicQueryObservation
	statusCode     int
	contentType    string
	actionSequence int
}

type requestDecision struct {
	allow bool
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

func (t *requestTracker) closeAndWait(timeout time.Duration) bool {
	t.mu.Lock()
	t.open = false
	t.mu.Unlock()
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
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
	if (request.BrowserQuery == nil) != (request.ListingAdvance == nil) ||
		(request.BrowserQuery != nil && (request.BrowserQuery.Validate() != nil || request.ListingAdvance.Validate() != nil || request.OnListingBatch == nil)) {
		return browserdriver.SessionResult{}, policyFailure(errors.New("browser listing advancement contract is incomplete"))
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

	state := &policyState{initialOrigin: origin(endpoint), maxNavigations: request.Plan.MaxNavigations,
		methods: map[string]struct{}{}, blockedMethods: map[string]struct{}{},
		publicQueries:         map[string]recipeabi.PublicQueryObservation{},
		pendingPublicQueries:  map[string]recipeabi.PublicQueryObservation{},
		pendingQueryResponses: map[string]publicQueryResponseMetadata{}, pendingQueryActions: map[string]int{},
		responseSignal: make(chan struct{}, 1)}
	requestTasks := newRequestTracker()
	chromedp.ListenTarget(tabCtx, func(event any) {
		switch value := event.(type) {
		case *fetch.EventRequestPaused:
			requestTasks.start(func() {
				decision := state.inspectRequest(tabCtx, r, value)
				_ = chromedp.Run(tabCtx, chromedp.ActionFunc(func(commandCtx context.Context) error {
					if decision.allow {
						return fetch.ContinueRequest(value.RequestID).Do(commandCtx)
					}
					return fetch.FailRequest(value.RequestID, network.ErrorReasonBlockedByClient).Do(commandCtx)
				}))
			})
		case *network.EventResponseReceived:
			state.observePublicQueryResponse(string(value.RequestID), int(value.Response.Status), value.Response.MimeType)
		case *network.EventLoadingFinished:
			requestTasks.start(func() { state.capturePublicQueryResponse(tabCtx, value.RequestID) })
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

	preventEffects := `(()=>{globalThis.__atollBlockedDownloads=0;globalThis.__atollBlockedPopups=0;globalThis.__atollCaptureJobPopup=false;globalThis.__atollJobPopupTarget="";const stop=e=>{e.preventDefault();e.stopImmediatePropagation()};document.addEventListener('submit',stop,true);document.addEventListener('click',e=>{const a=e.target&&e.target.closest&&e.target.closest('a[download]');if(a){globalThis.__atollBlockedDownloads++;stop(e)}},true);window.open=url=>{if(globalThis.__atollCaptureJobPopup){globalThis.__atollJobPopupTarget=String(url||"");return null}globalThis.__atollBlockedPopups++;return null}})()`
	setupErr := chromedp.Run(tabCtx,
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{
			{URLPattern: "*", RequestStage: fetch.RequestStageRequest},
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
	listingEnd, listingStop, advanceCount := false, "", 0
	if navigationErr == nil && request.ListingAdvance != nil {
		listingEnd, listingStop, advanceCount, navigationErr = runListingAdvancement(tabCtx, state, request)
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
	requestsDrained := requestTasks.closeAndWait(5 * time.Second)
	_ = chromedp.Cancel(tabCtx)
	cancelTab()
	state.recordBlockedEffects(blockedDownloads, blockedPopups)
	attestation, violation := state.attestation(request.Policy.TermsPolicyVersion, robotsAllowed, profiled)
	if int64(len(dom)) > request.Plan.MaxDOMBytes {
		dom = dom[:request.Plan.MaxDOMBytes+1]
	}
	result := browserdriver.SessionResult{FinalURL: finalURL, ContentType: contentType, DOM: []byte(dom), Attestation: attestation,
		PublicQueryResponses: state.publicQueryResponseEvidence(), EndOfInput: listingEnd, StopReason: listingStop,
		AdvanceCount: advanceCount}
	if violation != nil {
		return result, policyFailure(violation)
	}
	if navigationErr != nil {
		return result, fmt.Errorf("execute browser navigation: %w", navigationErr)
	}
	if captureErr != nil {
		return result, fmt.Errorf("capture browser DOM: %w", captureErr)
	}
	if !requestsDrained {
		return result, fmt.Errorf("browser request-policy handlers did not drain before their bound")
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

func runListingAdvancement(ctx context.Context, state *policyState,
	request browserdriver.SessionRequest) (bool, string, int, error) {
	contract, query := *request.ListingAdvance, *request.BrowserQuery
	rawIndex, delivered, lastHash := 0, 0, ""
	deliver := func(actionSequence int, timeout time.Duration) (bool, bool, error) {
		deadline := time.NewTimer(timeout)
		defer deadline.Stop()
		madeProgress := false
		for {
			responses, next := state.matchingResponses(rawIndex, query, actionSequence)
			rawIndex = next
			for _, response := range responses {
				if response.ContentHash == lastHash {
					continue
				}
				lastHash = response.ContentHash
				delivered++
				stop, err := request.OnListingBatch(response)
				if errors.Is(err, browserdriver.ErrListingNoProgress) {
					continue
				}
				if err != nil || stop {
					return true, stop, err
				}
				madeProgress = true
			}
			if len(responses) > 0 {
				return madeProgress, false, nil
			}
			select {
			case <-ctx.Done():
				return false, false, ctx.Err()
			case <-deadline.C:
				return false, false, nil
			case <-state.responseSignal:
			}
		}
	}

	got, stop, err := deliver(0, time.Duration(contract.WaitTimeoutMS)*time.Millisecond)
	if err != nil {
		return false, "initial_batch_failed", 0, err
	}
	if stop {
		return false, "safe_boundary", 0, nil
	}
	if !got || delivered == 0 {
		return false, "initial_batch_timeout", 0, &brokerFailure{class: "parse_error", cause: errors.New("declared listing response did not arrive")}
	}
	if contract.Kind == recipeabi.ListingAdvanceNone {
		return true, "end_of_input", 0, nil
	}
	if ended, proofErr := listingResponseEnded(state.lastMatchingResponse(query, 0), contract.EndProof); proofErr != nil {
		return false, "end_proof_invalid", 0, proofErr
	} else if ended {
		return true, "end_of_input", 0, nil
	}

	noProgress := 0
	for sequence := 1; sequence <= contract.MaxAdvances; sequence++ {
		state.setActionSequence(sequence)
		if actionErr := runListingAdvanceAction(ctx, contract); actionErr != nil {
			if contract.EndProof.Kind == "selector_absent_or_disabled" {
				ended, inspectErr := selectorAbsentOrDisabled(ctx, contract.EndProof.Selector)
				if inspectErr != nil {
					return false, "end_proof_invalid", sequence - 1, inspectErr
				}
				if ended {
					return true, "end_of_input", sequence - 1, nil
				}
			}
			return false, "advance_action_failed", sequence - 1, &brokerFailure{class: "parse_error", cause: actionErr}
		}
		got, stop, err = deliver(sequence, time.Duration(contract.WaitTimeoutMS)*time.Millisecond)
		if err != nil {
			return false, "batch_consumer_failed", sequence, err
		}
		if stop {
			return false, "safe_boundary", sequence, nil
		}
		if !got {
			noProgress++
			if contract.EndProof.Kind == "selector_absent_or_disabled" {
				ended, inspectErr := selectorAbsentOrDisabled(ctx, contract.EndProof.Selector)
				if inspectErr != nil {
					return false, "end_proof_invalid", sequence, inspectErr
				}
				if ended {
					return true, "end_of_input", sequence, nil
				}
			}
			if contract.EndProof.Kind == "stable_no_progress" && noProgress >= contract.MaxNoProgress {
				return true, "end_of_input", sequence, nil
			}
			if noProgress >= contract.MaxNoProgress {
				return false, "no_progress", sequence, &brokerFailure{class: "parse_error", cause: errors.New("listing advance produced no matching response")}
			}
			continue
		}
		noProgress = 0
		if ended, proofErr := listingResponseEnded(state.lastMatchingResponse(query, sequence), contract.EndProof); proofErr != nil {
			return false, "end_proof_invalid", sequence, proofErr
		} else if ended {
			return true, "end_of_input", sequence, nil
		}
	}
	return false, "bounded_incomplete", contract.MaxAdvances, nil
}

func (s *policyState) setActionSequence(sequence int) {
	s.mu.Lock()
	s.currentActionSequence = sequence
	s.mu.Unlock()
}

func (s *policyState) matchingResponses(index int, query recipeabi.BrowserQuery,
	actionSequence int) ([]browserdriver.PublicQueryResponse, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index > len(s.publicQueryResponses) {
		index = len(s.publicQueryResponses)
	}
	var latest *browserdriver.PublicQueryResponse
	for _, response := range s.publicQueryResponses[index:] {
		endpoint, err := url.Parse(response.Request.EndpointURL)
		if err == nil && response.ActionSequence == actionSequence && response.Request.Method == query.Method && endpoint.Path == query.EndpointPath {
			copy := response
			latest = &copy
		}
	}
	if latest == nil {
		return nil, len(s.publicQueryResponses)
	}
	return []browserdriver.PublicQueryResponse{*latest}, len(s.publicQueryResponses)
}

func (s *policyState) lastMatchingResponse(query recipeabi.BrowserQuery,
	actionSequence int) browserdriver.PublicQueryResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := len(s.publicQueryResponses) - 1; index >= 0; index-- {
		response := s.publicQueryResponses[index]
		endpoint, err := url.Parse(response.Request.EndpointURL)
		if err == nil && response.ActionSequence == actionSequence && response.Request.Method == query.Method && endpoint.Path == query.EndpointPath {
			return response
		}
	}
	return browserdriver.PublicQueryResponse{}
}

func runListingAdvanceAction(ctx context.Context, contract recipeabi.ListingAdvanceContract) error {
	switch contract.Kind {
	case recipeabi.ListingAdvanceClick:
		// The listing response normally arrives before the framework has committed
		// the matching pagination control to the DOM. Treating that short render
		// gap as an absent-control end proof truncates every production scan to its
		// first batch. Wait within the Recipe's bounded action budget before
		// deciding whether the control is genuinely absent.
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(contract.WaitTimeoutMS)*time.Millisecond)
		defer cancel()
		if err := chromedp.Run(waitCtx, chromedp.WaitReady(contract.Selector, chromedp.ByQuery)); err != nil {
			return errors.New("listing click target is missing")
		}
		expression := `(()=>{const n=document.querySelector(` + strconv.Quote(contract.Selector) + `);if(!n)return false;n.click();return true})()`
		var found bool
		if err := chromedp.Run(waitCtx, chromedp.Evaluate(expression, &found)); err != nil {
			return err
		}
		if !found {
			return errors.New("listing click target is missing")
		}
		return nil
	case recipeabi.ListingAdvanceScrollPage:
		return chromedp.Run(ctx, chromedp.Evaluate(`window.scrollTo(0,document.documentElement.scrollHeight)`, nil))
	case recipeabi.ListingAdvanceScrollContainer:
		expression := `(()=>{const n=document.querySelector(` + strconv.Quote(contract.Selector) + `);if(!n)return false;n.scrollTop=n.scrollHeight;return true})()`
		var found bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &found)); err != nil {
			return err
		}
		if !found {
			return errors.New("listing scroll container is missing")
		}
		return nil
	default:
		return fmt.Errorf("unsupported listing advance action %q", contract.Kind)
	}
}

func selectorAbsentOrDisabled(ctx context.Context, selector string) (bool, error) {
	expression := `(()=>{const n=document.querySelector(` + strconv.Quote(selector) + `);return !n||n.disabled===true||n.getAttribute('aria-disabled')==='true'||[...n.classList].some(c=>c==='disabled'||c.endsWith('-disabled'))})()`
	var ended bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(expression, &ended)); err != nil {
		return false, err
	}
	return ended, nil
}

func listingResponseEnded(response browserdriver.PublicQueryResponse, proof recipeabi.ListingEndProof) (bool, error) {
	if proof.Kind != "response_false" && proof.Kind != "response_empty" {
		return false, nil
	}
	var document any
	if err := json.Unmarshal(response.Body, &document); err != nil {
		return false, fmt.Errorf("decode listing end proof: %w", err)
	}
	value, found := jsonPointerValue(document, proof.Pointer)
	if !found {
		return false, fmt.Errorf("listing end pointer %q is missing", proof.Pointer)
	}
	if proof.Kind == "response_false" {
		flag, ok := value.(bool)
		if !ok {
			return false, fmt.Errorf("listing end pointer %q is not boolean", proof.Pointer)
		}
		return !flag, nil
	}
	switch candidate := value.(type) {
	case nil:
		return true, nil
	case string:
		return strings.TrimSpace(candidate) == "", nil
	case []any:
		return len(candidate) == 0, nil
	default:
		return false, fmt.Errorf("listing end pointer %q is not empty-checkable", proof.Pointer)
	}
}

func jsonPointerValue(document any, pointer string) (any, bool) {
	if pointer == "" {
		return document, true
	}
	current := document
	for _, raw := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		key := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			var found bool
			current, found = node[key]
			if !found {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
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
		var candidate struct {
			Href  string `json:"href"`
			JobID string `json:"job_id"`
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(`(()=>{const node=document.querySelector(`+selector+`);return {href:node&&node.href||"",job_id:node&&node.getAttribute&&node.getAttribute("data-jobunionid")||""}})()`, &candidate)); err != nil {
			return err
		}
		if candidate.Href != "" {
			next, err := runner.publicURL(ctx, candidate.Href)
			if err != nil || origin(next) != origin(initial) {
				return policyFailure(errors.New("follow_link target is missing, non-public, or cross-origin"))
			}
			return chromedp.Run(ctx, chromedp.Navigate(next.String()))
		}
		if strings.TrimSpace(candidate.JobID) == "" {
			return policyFailure(errors.New("follow_link target is neither a public link nor an identified job element"))
		}
		// Some recruitment sites expose job cards as SPA-controlled divs. The
		// explicit stable job identity is required before activation; the Broker
		// still blocks writes, downloads, popups, and cross-origin documents.
		if err := chromedp.Run(ctx,
			chromedp.Evaluate(`globalThis.__atollJobPopupTarget="";globalThis.__atollCaptureJobPopup=true`, nil),
			chromedp.Click(action.Selector, chromedp.ByQuery),
			chromedp.Evaluate(`globalThis.__atollCaptureJobPopup=false`, nil),
			chromedp.Sleep(500*time.Millisecond)); err != nil {
			return err
		}
		var popupTarget string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`globalThis.__atollJobPopupTarget||""`, &popupTarget)); err != nil {
			return err
		}
		if popupTarget != "" {
			reference, parseErr := url.Parse(popupTarget)
			if parseErr != nil {
				return policyFailure(errors.New("identified job element produced an invalid popup target"))
			}
			next, publicErr := runner.publicURL(ctx, initial.ResolveReference(reference).String())
			if publicErr != nil || origin(next) != origin(initial) {
				return policyFailure(errors.New("identified job element produced a non-public or cross-origin popup target"))
			}
			return chromedp.Run(ctx, chromedp.Navigate(next.String()))
		}
		var currentURL string
		if err := chromedp.Run(ctx, chromedp.Location(&currentURL)); err != nil {
			return err
		}
		current, err := runner.publicURL(ctx, currentURL)
		if err != nil || origin(current) != origin(initial) {
			return policyFailure(errors.New("identified job element navigated outside the public origin"))
		}
		return nil
	default:
		return fmt.Errorf("unsupported browser action %q", action.Kind)
	}
}

func (s *policyState) inspectRequest(ctx context.Context, runner *Runner, event *fetch.EventRequestPaused) requestDecision {
	if event.ResponseStatusCode != 0 || event.ResponseErrorReason != "" {
		for _, header := range event.ResponseHeaders {
			if strings.EqualFold(strings.TrimSpace(header.Name), "Content-Disposition") &&
				strings.Contains(strings.ToLower(header.Value), "attachment") {
				s.mu.Lock()
				s.downloads++
				s.setViolation(errors.New("page attempted a download"))
				s.mu.Unlock()
				return requestDecision{}
			}
		}
		return requestDecision{allow: true}
	}
	method := strings.ToUpper(strings.TrimSpace(event.Request.Method))
	// OPTIONS is a read-only capability/preflight probe used by modern sites.
	// A POST may proceed only when it is an XHR/fetch whose URL, headers and
	// bounded JSON body satisfy the public listing-query contract. The browser
	// owns any runtime signature and cookies; Atoll captures the response in the
	// same session instead of replaying the request through an HTTP client.
	allowed := method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
	if !allowed {
		if method == http.MethodPost && (event.ResourceType == network.ResourceTypeXHR || event.ResourceType == network.ResourceTypeFetch) {
			if endpoint, endpointErr := runner.publicURL(ctx, event.Request.URL); endpointErr == nil &&
				origin(endpoint) == s.initialOrigin && isBrowserSessionBootstrap(endpoint) {
				s.mu.Lock()
				s.methods[method] = struct{}{}
				s.allowedPublicQueries++
				s.mu.Unlock()
				return requestDecision{allow: true}
			}
			if observation, err := runner.publicQueryObservation(ctx, event); err == nil {
				parsed, parseErr := runner.publicURL(ctx, observation.EndpointURL)
				if parseErr == nil && origin(parsed) == s.initialOrigin {
					requestID := string(event.NetworkID)
					if requestID == "" {
						requestID = string(event.RequestID)
					}
					s.addPublicQuery(requestID, observation)
					s.mu.Lock()
					s.methods[method] = struct{}{}
					s.allowedPublicQueries++
					s.mu.Unlock()
					return requestDecision{allow: true}
				}
			}
		}
		s.mu.Lock()
		s.blockedWriteRequests++
		s.blockedMethods[method] = struct{}{}
		if event.ResourceType == network.ResourceTypeDocument {
			s.formSubmissions++
			s.setViolation(fmt.Errorf("browser document request used unsafe method %s", method))
		}
		s.mu.Unlock()
		return requestDecision{}
	}
	s.mu.Lock()
	s.methods[method] = struct{}{}
	s.mu.Unlock()
	parsed, err := runner.publicURL(ctx, event.Request.URL)
	if err != nil {
		s.mu.Lock()
		s.setViolation(fmt.Errorf("browser request targeted a non-public URL"))
		s.mu.Unlock()
		return requestDecision{}
	}
	if event.ResourceType == network.ResourceTypeDocument {
		s.mu.Lock()
		s.documentNavigations++
		if s.documentNavigations > s.maxNavigations {
			s.setViolation(errors.New("browser document navigation exceeded its bound"))
			allowed = false
		} else if origin(parsed) != s.initialOrigin {
			s.crossOriginDocuments++
			s.setViolation(errors.New("browser document navigation crossed origin"))
			allowed = false
		}
		s.mu.Unlock()
	}
	return requestDecision{allow: allowed}
}

func isBrowserSessionBootstrap(endpoint *url.URL) bool {
	path := strings.ToLower(strings.TrimSuffix(endpoint.EscapedPath(), "/"))
	return strings.HasSuffix(path, "/csrf/token") || strings.HasSuffix(path, "/token/csrf")
}

func (r *Runner) publicQueryObservation(ctx context.Context, event *fetch.EventRequestPaused) (recipeabi.PublicQueryObservation, error) {
	endpoint, err := r.publicURL(ctx, event.Request.URL)
	if err != nil {
		return recipeabi.PublicQueryObservation{}, err
	}
	headers := make(map[string]string)
	for name, raw := range event.Request.Headers {
		canonical := strings.ToLower(strings.TrimSpace(name))
		// Cookies from this short-lived, empty public profile are deliberately
		// omitted. Authorization-bearing requests are not candidates at all.
		if canonical == "authorization" || canonical == "proxy-authorization" {
			return recipeabi.PublicQueryObservation{}, errors.New("credential-bearing request is not public-query evidence")
		}
		switch canonical {
		case "accept", "accept-language", "content-type", "website-path":
			value, ok := raw.(string)
			if !ok {
				return recipeabi.PublicQueryObservation{}, errors.New("public query header is not textual")
			}
			headers[http.CanonicalHeaderKey(canonical)] = value
		}
	}
	body := make([]byte, 0, 1024)
	for _, entry := range event.Request.PostDataEntries {
		if entry == nil {
			return recipeabi.PublicQueryObservation{}, errors.New("public query body contained an empty entry")
		}
		part, decodeErr := base64.StdEncoding.DecodeString(entry.Bytes)
		if decodeErr != nil || len(body)+len(part) > 64<<10 {
			return recipeabi.PublicQueryObservation{}, errors.New("public query body was invalid or exceeded its bound")
		}
		body = append(body, part...)
	}
	// Recent Chrome versions can omit postDataEntries from Fetch.requestPaused
	// even while HasPostData is true.  The paired Network request ID is the
	// protocol-supported fallback; without it, real read-only recruitment
	// searches are blocked but never become verifiable observations.
	if len(body) == 0 && event.Request.HasPostData && event.NetworkID != "" {
		var postData string
		postDataCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := chromedp.Run(postDataCtx, chromedp.ActionFunc(func(commandCtx context.Context) error {
			var err error
			postData, err = network.GetRequestPostData(event.NetworkID).Do(commandCtx)
			return err
		})); err != nil || len(postData) > 64<<10 {
			return recipeabi.PublicQueryObservation{}, errors.New("public query body was unavailable or exceeded its bound")
		}
		body = append(body, postData...)
	}
	if len(body) == 0 {
		body = append(body, '{', '}')
	}
	if _, exists := headers["Content-Type"]; !exists {
		headers["Content-Type"] = "application/json"
	}
	return recipeabi.NewPublicQueryObservation(endpoint.String(), http.MethodPost, headers, json.RawMessage(body))
}

func (s *policyState) addPublicQuery(requestID string, observation recipeabi.PublicQueryObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.publicQueries) >= 20 || s.publicQueryBytes+observation.EncodedSize() > recipeabi.MaxPublicQueryEvidenceBytes {
		return
	}
	key, err := observation.StableIdentityKey()
	if err != nil {
		return
	}
	if _, duplicate := s.publicQueries[key]; duplicate {
		s.pendingPublicQueries[requestID] = observation
		s.pendingQueryActions[requestID] = s.currentActionSequence
		return
	}
	s.publicQueries[key] = observation
	s.publicQueryBytes += observation.EncodedSize()
	s.pendingPublicQueries[requestID] = observation
	s.pendingQueryActions[requestID] = s.currentActionSequence
}

func (s *policyState) observePublicQueryResponse(requestID string, statusCode int, contentType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	observation, found := s.pendingPublicQueries[requestID]
	if !found {
		return
	}
	s.pendingQueryResponses[requestID] = publicQueryResponseMetadata{
		observation: observation, statusCode: statusCode, contentType: contentType,
		actionSequence: s.pendingQueryActions[requestID],
	}
}

func (s *policyState) capturePublicQueryResponse(ctx context.Context, requestID network.RequestID) {
	s.mu.Lock()
	metadata, found := s.pendingQueryResponses[string(requestID)]
	delete(s.pendingQueryResponses, string(requestID))
	delete(s.pendingPublicQueries, string(requestID))
	delete(s.pendingQueryActions, string(requestID))
	s.mu.Unlock()
	if !found || metadata.statusCode < 200 || metadata.statusCode > 299 {
		return
	}
	mediaType, _, err := mime.ParseMediaType(metadata.contentType)
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return
	}
	var body []byte
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(commandCtx context.Context) error {
		var bodyErr error
		body, bodyErr = network.GetResponseBody(requestID).Do(commandCtx)
		return bodyErr
	})); err != nil || len(body) == 0 || len(body) > browserdriver.MaxPublicQueryResponseBytes {
		return
	}
	sum := sha256.Sum256(body)
	response := browserdriver.PublicQueryResponse{Request: metadata.observation, StatusCode: metadata.statusCode,
		ContentType: metadata.contentType, Body: append([]byte(nil), body...), ContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		ActionSequence: metadata.actionSequence}
	if response.Validate() != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.publicQueryResponses) >= browserdriver.MaxPublicQueryResponseCount ||
		s.publicResponseBytes+len(body) > browserdriver.MaxPublicQueryResponseTotalBytes {
		return
	}
	s.publicQueryResponses = append(s.publicQueryResponses, response)
	s.publicResponseBytes += len(body)
	select {
	case s.responseSignal <- struct{}{}:
	default:
	}
}

func (s *policyState) publicQueryResponseEvidence() []browserdriver.PublicQueryResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]browserdriver.PublicQueryResponse, len(s.publicQueryResponses))
	copy(result, s.publicQueryResponses)
	return result
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
		RobotsAllowed: robotsAllowed, TermsPolicyVersion: termsVersion, ProfileLeaseAuthorized: profileLeaseAuthorized,
		AllowedPublicQueryRequests: s.allowedPublicQueries, CapturedPublicQueryResponses: len(s.publicQueryResponses)}, s.firstViolation
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
