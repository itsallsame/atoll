// Package browserdriver defines the constrained boundary to a browser broker.
// The broker, which may run near an authorized browser profile, resolves opaque
// profile references. Neither Atoll messages nor this driver receive cookies,
// passwords, OTPs, or other profile material.
package browserdriver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeexec"
)

const PlanVersion = recipeabi.BrowserPlanVersion

type ActionKind = recipeabi.BrowserActionKind
type Action = recipeabi.BrowserAction
type Plan = recipeabi.BrowserPlan

const (
	ActionWaitSelector = recipeabi.BrowserActionWaitSelector
	ActionScrollPage   = recipeabi.BrowserActionScrollPage
	ActionFollowLink   = recipeabi.BrowserActionFollowLink
)

type SessionRequest struct {
	EndpointURL    string                            `json:"endpoint_url"`
	UserAgent      string                            `json:"user_agent"`
	AcceptLanguage string                            `json:"accept_language,omitempty"`
	ProfileRef     string                            `json:"profile_ref,omitempty"`
	ProfileVersion uint64                            `json:"profile_version,omitempty"`
	Plan           Plan                              `json:"plan"`
	PlanHash       string                            `json:"plan_hash"`
	AttemptID      string                            `json:"attempt_id"`
	TimeoutMS      int                               `json:"timeout_ms"`
	Policy         PolicyEvidence                    `json:"policy"`
	AllowedMethods []string                          `json:"allowed_methods"`
	SameOriginDocs bool                              `json:"same_origin_documents"`
	BlockDownloads bool                              `json:"block_downloads"`
	BlockPopups    bool                              `json:"block_popups"`
	BrowserQuery   *recipeabi.BrowserQuery           `json:"browser_query,omitempty"`
	ListingAdvance *recipeabi.ListingAdvanceContract `json:"listing_advance,omitempty"`
	OnListingBatch ListingBatchConsumer              `json:"-"`
}

type ListingBatchConsumer func(PublicQueryResponse) (stop bool, err error)

var ErrListingNoProgress = errors.New("listing batch contained no new stable job identity")

const (
	MaxPublicQueryResponseBytes      = 20 << 20
	MaxPublicQueryResponseCount      = 200
	MaxPublicQueryResponseTotalBytes = 200 << 20
)

// PublicQueryResponse is a bounded JSON response observed in the same browser
// session that created the request. Runtime signatures, cookies, Origin and
// Referer therefore remain browser-owned and are never replayed by Atoll.
type PublicQueryResponse struct {
	Request        recipeabi.PublicQueryObservation `json:"request"`
	StatusCode     int                              `json:"status_code"`
	ContentType    string                           `json:"content_type"`
	Body           []byte                           `json:"-"`
	ContentHash    string                           `json:"content_hash"`
	ActionSequence int                              `json:"action_sequence"`
}

func (s PublicQueryResponse) Validate() error {
	if err := s.Request.Validate(); err != nil {
		return fmt.Errorf("validate browser public query request: %w", err)
	}
	if s.StatusCode < 200 || s.StatusCode > 299 || len(s.Body) == 0 || len(s.Body) > MaxPublicQueryResponseBytes || s.ActionSequence < 0 {
		return fmt.Errorf("browser public query requires one bounded successful response")
	}
	contentType, _, err := mime.ParseMediaType(strings.TrimSpace(s.ContentType))
	if err != nil || contentType != "application/json" {
		return fmt.Errorf("browser public query response must be JSON")
	}
	sum := sha256.Sum256(s.Body)
	if s.ContentHash != "sha256:"+hex.EncodeToString(sum[:]) {
		return fmt.Errorf("browser public query response hash mismatch")
	}
	return nil
}

type Attestation struct {
	DocumentNavigations            int      `json:"document_navigations"`
	ObservedMethods                []string `json:"observed_methods"`
	BlockedMethods                 []string `json:"blocked_methods,omitempty"`
	AllowedWriteRequests           int      `json:"allowed_write_requests"`
	BlockedWriteRequests           int      `json:"blocked_write_requests,omitempty"`
	CrossOriginDocumentNavigations int      `json:"cross_origin_document_navigations"`
	FormSubmissions                int      `json:"form_submissions"`
	Downloads                      int      `json:"downloads"`
	Popups                         int      `json:"popups"`
	PublicEndpoint                 bool     `json:"public_endpoint"`
	RobotsAllowed                  bool     `json:"robots_allowed"`
	TermsPolicyVersion             uint64   `json:"terms_policy_version"`
	ProfileLeaseAuthorized         bool     `json:"profile_lease_authorized"`
	AllowedPublicQueryRequests     int      `json:"allowed_public_query_requests,omitempty"`
	CapturedPublicQueryResponses   int      `json:"captured_public_query_responses,omitempty"`
}

func (a Attestation) Validate(request SessionRequest) error {
	if a.DocumentNavigations < 1 || a.DocumentNavigations > request.Plan.MaxNavigations ||
		a.AllowedWriteRequests != 0 || a.BlockedWriteRequests < 0 || a.CrossOriginDocumentNavigations != 0 || a.FormSubmissions != 0 ||
		a.Downloads != 0 || a.Popups != 0 || !a.PublicEndpoint || !a.RobotsAllowed ||
		a.TermsPolicyVersion != request.Policy.TermsPolicyVersion ||
		(request.ProfileRef != "" && !a.ProfileLeaseAuthorized) {
		return fmt.Errorf("browser broker violated effect policy")
	}
	postObserved := false
	for _, method := range a.ObservedMethods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == http.MethodPost {
			postObserved = true
		} else if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
			return fmt.Errorf("browser broker observed unsafe method %q", method)
		}
	}
	if postObserved != (a.AllowedPublicQueryRequests > 0) || a.CapturedPublicQueryResponses > a.AllowedPublicQueryRequests {
		return fmt.Errorf("browser broker returned inconsistent public query evidence")
	}
	for _, method := range a.BlockedMethods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" || method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			return fmt.Errorf("browser broker returned invalid blocked method %q", method)
		}
	}
	if (a.BlockedWriteRequests == 0) != (len(a.BlockedMethods) == 0) {
		return fmt.Errorf("browser broker returned inconsistent blocked write evidence")
	}
	return nil
}

type SessionResult struct {
	FinalURL             string                `json:"final_url"`
	ContentType          string                `json:"content_type"`
	DOM                  []byte                `json:"-"`
	Attestation          Attestation           `json:"attestation"`
	PublicQueryResponses []PublicQueryResponse `json:"public_query_responses,omitempty"`
	EndOfInput           bool                  `json:"end_of_input,omitempty"`
	StopReason           string                `json:"stop_reason,omitempty"`
	AdvanceCount         int                   `json:"advance_count,omitempty"`
}

type Broker interface {
	Run(context.Context, SessionRequest) (SessionResult, error)
}

type PolicyEvidence struct {
	TermsPolicyVersion uint64 `json:"terms_policy_version"`
	TermsReviewedAt    string `json:"terms_reviewed_at"`
}

func (e PolicyEvidence) Validate() error {
	if e.TermsPolicyVersion == 0 {
		return fmt.Errorf("browser terms policy version is required")
	}
	if _, err := time.Parse(time.RFC3339, e.TermsReviewedAt); err != nil {
		return fmt.Errorf("browser terms review time must be RFC3339")
	}
	return nil
}

type ArtifactWrite struct {
	Kind         string      `json:"kind"`
	AttemptID    string      `json:"attempt_id"`
	URL          string      `json:"url"`
	ContentType  string      `json:"content_type,omitempty"`
	ContentHash  string      `json:"content_hash"`
	Attestation  Attestation `json:"attestation"`
	Body         []byte      `json:"-"`
	PageSequence int         `json:"page_sequence,omitempty"`
}

type ArtifactSink interface {
	Put(context.Context, ArtifactWrite) (recipeabi.ArtifactRef, error)
}

type Driver struct{ broker Broker }

func New(broker Broker) (*Driver, error) {
	if broker == nil {
		return nil, fmt.Errorf("browser broker is required")
	}
	return &Driver{broker: broker}, nil
}

type PageResult struct {
	Document       recipeexec.DocumentResult
	Artifact       recipeabi.ArtifactRef
	FinalURL       string
	Attestation    Attestation
	ActionSequence int
}

type RunError struct {
	Class string
	Cause error
}

func (e *RunError) Error() string { return fmt.Sprintf("browser driver %s: %v", e.Class, e.Cause) }
func (e *RunError) Unwrap() error { return e.Cause }

type ClassifiedBrokerError interface {
	error
	BrowserFailureClass() string
}

func (d *Driver) ExecutePage(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput, plan Plan, policy PolicyEvidence, sink ArtifactSink) (PageResult, error) {
	if sink == nil {
		return PageResult{}, fmt.Errorf("browser artifact sink is required")
	}
	if err := spec.Validate(); err != nil {
		return PageResult{}, err
	}
	if spec.Transport != recipeabi.TransportBrowser && spec.Transport != recipeabi.TransportBrowserJSON {
		return PageResult{}, fmt.Errorf("browser driver requires browser transport")
	}
	canonicalPlanHash := ""
	if spec.BrowserPlan != nil {
		canonicalPlanHash, _ = spec.BrowserPlan.ContentHash()
	}
	requestedPlanHash, _ := plan.ContentHash()
	if spec.BrowserPlan == nil || canonicalPlanHash == "" || canonicalPlanHash != requestedPlanHash {
		return PageResult{}, fmt.Errorf("browser execution plan must match the immutable Recipe")
	}
	offlineSpec := spec
	offlineSpec.BrowserQuery = nil
	offlineSpec.ListingAdvance = nil
	if spec.Transport == recipeabi.TransportBrowserJSON {
		offlineSpec.Transport = recipeabi.TransportHTTPJSON
	} else {
		offlineSpec.Transport = recipeabi.TransportHTTPHTML
	}
	offlineSpec.BrowserPlan = nil
	if err := offlineSpec.Validate(); err != nil {
		return PageResult{}, err
	}
	if err := input.Validate(); err != nil {
		return PageResult{}, err
	}
	if err := policy.Validate(); err != nil {
		return PageResult{}, err
	}
	planHash, err := plan.ContentHash()
	if err != nil {
		return PageResult{}, err
	}
	request := SessionRequest{EndpointURL: input.Endpoint.URL, UserAgent: spec.Request.UserAgent,
		AcceptLanguage: spec.Request.Headers["Accept-Language"], ProfileRef: input.ProfileRef,
		ProfileVersion: input.Attempt.ProfileVersion,
		Plan:           plan, PlanHash: planHash, AttemptID: input.Attempt.AttemptID,
		TimeoutMS:      spec.Request.TimeoutMS,
		Policy:         policy,
		AllowedMethods: []string{http.MethodGet, http.MethodHead, http.MethodOptions}, SameOriginDocs: true, BlockDownloads: true, BlockPopups: true}
	runContext, cancel := context.WithTimeout(ctx, time.Duration(spec.Request.TimeoutMS)*time.Millisecond)
	defer cancel()
	session, brokerErr := d.broker.Run(runContext, request)
	body := session.DOM
	artifactURL := session.FinalURL
	artifactContentType := session.ContentType
	if spec.Transport == recipeabi.TransportBrowserJSON {
		body = nil
		for _, captured := range session.PublicQueryResponses {
			if spec.BrowserQuery.MatchesObservation(captured.Request) {
				body = captured.Body
				artifactURL = captured.Request.EndpointURL
				artifactContentType = captured.ContentType
				break
			}
		}
		if len(body) == 0 && brokerErr == nil {
			brokerErr = &brokerFailureAdapter{class: "parse_error", cause: fmt.Errorf("browser did not emit the declared public query response")}
		}
	}
	limit := spec.Request.MaxResponseBytes
	if spec.Transport != recipeabi.TransportBrowserJSON && plan.MaxDOMBytes < limit {
		limit = plan.MaxDOMBytes
	}
	tooLarge := int64(len(body)) > limit
	if tooLarge && int64(len(body)) > limit {
		body = body[:limit]
	}
	sum := sha256.Sum256(body)
	contentHash := "sha256:" + hex.EncodeToString(sum[:])
	if strings.TrimSpace(artifactURL) == "" {
		artifactURL = input.Endpoint.URL
	}
	artifact, artifactErr := sink.Put(ctx, ArtifactWrite{Kind: "browser_dom", AttemptID: input.Attempt.AttemptID,
		URL: artifactURL, ContentType: artifactContentType, ContentHash: contentHash, Attestation: session.Attestation, Body: body})
	if artifactErr != nil {
		return PageResult{}, fmt.Errorf("save browser artifact before parsing: %w", artifactErr)
	}
	if strings.TrimSpace(artifact.ArtifactID) == "" || !strings.HasPrefix(artifact.ContentHash, "sha256:") ||
		strings.TrimSpace(artifact.ObjectRef) == "" {
		return PageResult{}, fmt.Errorf("browser artifact sink returned an invalid reference")
	}
	if artifact.ContentHash != contentHash {
		return PageResult{}, fmt.Errorf("browser artifact sink changed content hash")
	}
	result := PageResult{Artifact: artifact, FinalURL: session.FinalURL, Attestation: session.Attestation}
	if brokerErr != nil {
		var classified ClassifiedBrokerError
		if errors.As(brokerErr, &classified) {
			switch class := classified.BrowserFailureClass(); class {
			case "effect_policy_violated", "endpoint_rejected", "robots_disallowed", "auth_expired", "captcha", "parse_error":
				return result, &RunError{Class: class, Cause: brokerErr}
			}
		}
		return result, &RunError{Class: "browser_transport", Cause: brokerErr}
	}
	if tooLarge {
		return result, &RunError{Class: "response_too_large", Cause: fmt.Errorf("browser DOM exceeded byte budget")}
	}
	if err := session.Attestation.Validate(request); err != nil {
		return result, &RunError{Class: "effect_policy_violated", Cause: err}
	}
	initialURL, _ := url.Parse(input.Endpoint.URL)
	finalURL, parseErr := url.Parse(session.FinalURL)
	if parseErr != nil || finalURL.User != nil || finalURL.Fragment != "" || finalURL.Scheme != initialURL.Scheme ||
		!strings.EqualFold(finalURL.Host, initialURL.Host) {
		return result, &RunError{Class: "redirect_rejected", Cause: fmt.Errorf("browser final document crossed origin")}
	}
	var document recipeexec.DocumentResult
	if spec.Transport == recipeabi.TransportBrowserJSON {
		document, err = recipeexec.ExecuteJSON(offlineSpec, body)
	} else {
		document, err = recipeexec.ExecuteHTML(offlineSpec, body)
	}
	if err != nil {
		return result, &RunError{Class: "parse_error", Cause: err}
	}
	result.Document = document
	return result, nil
}

// ExecuteListing keeps one browser session alive while the immutable
// ListingAdvanceContract emits batches. Every matching response is persisted
// before it is parsed and delivered to the caller.
func (d *Driver) ExecuteListing(ctx context.Context, spec recipeabi.Spec, input recipeabi.RunInput, plan Plan,
	policy PolicyEvidence, sink ArtifactSink, consume func(PageResult) (bool, error)) (SessionResult, []PageResult, error) {
	if sink == nil || consume == nil {
		return SessionResult{}, nil, fmt.Errorf("browser listing requires artifact sink and batch consumer")
	}
	if err := spec.Validate(); err != nil {
		return SessionResult{}, nil, err
	}
	if spec.Kind != recipeabi.KindListing || spec.Transport != recipeabi.TransportBrowserJSON ||
		spec.BrowserPlan == nil || spec.BrowserQuery == nil || spec.ListingAdvance == nil {
		return SessionResult{}, nil, fmt.Errorf("multi-batch browser execution requires a browser JSON Listing Recipe")
	}
	canonicalPlanHash, _ := spec.BrowserPlan.ContentHash()
	requestedPlanHash, _ := plan.ContentHash()
	if canonicalPlanHash == "" || canonicalPlanHash != requestedPlanHash {
		return SessionResult{}, nil, fmt.Errorf("browser execution plan must match the immutable Recipe")
	}
	if err := input.Validate(); err != nil {
		return SessionResult{}, nil, err
	}
	if err := policy.Validate(); err != nil {
		return SessionResult{}, nil, err
	}
	offlineSpec := spec
	offlineSpec.Transport = recipeabi.TransportHTTPJSON
	offlineSpec.BrowserPlan, offlineSpec.BrowserQuery, offlineSpec.ListingAdvance = nil, nil, nil
	if err := offlineSpec.Validate(); err != nil {
		return SessionResult{}, nil, err
	}
	planHash, err := plan.ContentHash()
	if err != nil {
		return SessionResult{}, nil, err
	}
	request := SessionRequest{EndpointURL: input.Endpoint.URL, UserAgent: spec.Request.UserAgent,
		AcceptLanguage: spec.Request.Headers["Accept-Language"], ProfileRef: input.ProfileRef,
		ProfileVersion: input.Attempt.ProfileVersion, Plan: plan, PlanHash: planHash,
		AttemptID: input.Attempt.AttemptID, TimeoutMS: spec.Request.TimeoutMS, Policy: policy,
		AllowedMethods: []string{http.MethodGet, http.MethodHead, http.MethodOptions}, SameOriginDocs: true,
		BlockDownloads: true, BlockPopups: true, BrowserQuery: spec.BrowserQuery, ListingAdvance: spec.ListingAdvance}
	pages := make([]PageResult, 0, spec.Listing.MaxPages)
	processed := map[string]struct{}{}
	var totalBytes int64
	process := func(captured PublicQueryResponse) (bool, error) {
		if !spec.BrowserQuery.MatchesObservation(captured.Request) {
			return false, nil
		}
		if err := captured.Validate(); err != nil {
			return false, err
		}
		key := fmt.Sprintf("%d:%s", captured.ActionSequence, captured.ContentHash)
		if _, duplicate := processed[key]; duplicate {
			return false, nil
		}
		processed[key] = struct{}{}
		if len(pages) >= spec.Listing.MaxPages {
			return true, nil
		}
		body := captured.Body
		totalBytes += int64(len(body))
		if totalBytes > spec.Listing.MaxTotalBytes {
			return false, &RunError{Class: "response_too_large", Cause: fmt.Errorf("browser listing exceeded total byte budget")}
		}
		if int64(len(body)) > spec.Request.MaxResponseBytes {
			return false, &RunError{Class: "response_too_large", Cause: fmt.Errorf("browser response exceeded byte budget")}
		}
		artifact, putErr := sink.Put(ctx, ArtifactWrite{Kind: "browser_json", AttemptID: input.Attempt.AttemptID,
			URL: captured.Request.EndpointURL, ContentType: captured.ContentType, ContentHash: captured.ContentHash,
			Attestation: Attestation{}, Body: body, PageSequence: len(pages) + 1})
		if putErr != nil {
			return false, fmt.Errorf("save browser batch before parsing: %w", putErr)
		}
		document, parseErr := recipeexec.ExecuteJSON(offlineSpec, body)
		if parseErr != nil {
			return false, &RunError{Class: "parse_error", Cause: parseErr}
		}
		// Browser advancement owns termination; an absent JSON `next` field is
		// not evidence that this UI-backed listing ended.
		document.Next = json.RawMessage(`"browser_advance"`)
		page := PageResult{Document: document, Artifact: artifact, FinalURL: captured.Request.EndpointURL,
			ActionSequence: captured.ActionSequence}
		pages = append(pages, page)
		return consume(page)
	}
	request.OnListingBatch = process
	runContext, cancel := context.WithTimeout(ctx, time.Duration(spec.Request.TimeoutMS)*time.Millisecond)
	defer cancel()
	session, brokerErr := d.broker.Run(runContext, request)
	if brokerErr == nil && len(pages) == 0 {
		for _, captured := range session.PublicQueryResponses {
			stop, consumeErr := process(captured)
			if consumeErr != nil {
				brokerErr = consumeErr
				break
			}
			if stop {
				break
			}
		}
	}
	if brokerErr != nil {
		var runErr *RunError
		if errors.As(brokerErr, &runErr) {
			return session, pages, runErr
		}
		var classified ClassifiedBrokerError
		if errors.As(brokerErr, &classified) {
			return session, pages, &RunError{Class: classified.BrowserFailureClass(), Cause: brokerErr}
		}
		return session, pages, &RunError{Class: "browser_transport", Cause: brokerErr}
	}
	if len(pages) == 0 {
		return session, nil, &RunError{Class: "parse_error", Cause: fmt.Errorf("browser did not emit the declared public query response")}
	}
	if err := session.Attestation.Validate(request); err != nil {
		return session, pages, &RunError{Class: "effect_policy_violated", Cause: err}
	}
	return session, pages, nil
}

type brokerFailureAdapter struct {
	class string
	cause error
}

func (e *brokerFailureAdapter) Error() string               { return e.cause.Error() }
func (e *brokerFailureAdapter) Unwrap() error               { return e.cause }
func (e *brokerFailureAdapter) BrowserFailureClass() string { return e.class }
