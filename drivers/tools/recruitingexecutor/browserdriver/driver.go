// Package browserdriver defines the constrained boundary to a browser broker.
// The broker, which may run near an authorized browser profile, resolves opaque
// profile references. Neither Atoll messages nor this driver receive cookies,
// passwords, OTPs, or other profile material.
package browserdriver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	EndpointURL      string            `json:"endpoint_url"`
	UserAgent        string            `json:"user_agent"`
	AcceptLanguage   string            `json:"accept_language,omitempty"`
	ProfileRef       string            `json:"profile_ref,omitempty"`
	ProfileVersion   uint64            `json:"profile_version,omitempty"`
	Plan             Plan              `json:"plan"`
	PlanHash         string            `json:"plan_hash"`
	AttemptID        string            `json:"attempt_id"`
	TimeoutMS        int               `json:"timeout_ms"`
	Policy           PolicyEvidence    `json:"policy"`
	AllowedMethods   []string          `json:"allowed_methods"`
	SameOriginDocs   bool              `json:"same_origin_documents"`
	BlockDownloads   bool              `json:"block_downloads"`
	BlockPopups      bool              `json:"block_popups"`
	PublicQueryStubs []PublicQueryStub `json:"public_query_stubs,omitempty"`
}

const (
	MaxPublicQueryStubBytes      = 1 << 20
	MaxPublicQueryStubCount      = 10
	MaxPublicQueryStubTotalBytes = 4 << 20
)

// PublicQueryStub is a response captured by a separate, audited public HTTP
// verification. The browser may fulfill exactly one matching request locally;
// it never turns POST into an origin-side browser capability.
type PublicQueryStub struct {
	Request     recipeabi.PublicQueryObservation `json:"request"`
	StatusCode  int                              `json:"status_code"`
	ContentType string                           `json:"content_type"`
	Body        []byte                           `json:"-"`
	ContentHash string                           `json:"content_hash"`
}

func (s PublicQueryStub) Validate() error {
	if err := s.Request.Validate(); err != nil {
		return fmt.Errorf("validate public query stub request: %w", err)
	}
	if s.StatusCode < 200 || s.StatusCode > 299 || len(s.Body) == 0 || len(s.Body) > MaxPublicQueryStubBytes {
		return fmt.Errorf("public query stub requires one bounded successful response")
	}
	contentType, _, err := mime.ParseMediaType(strings.TrimSpace(s.ContentType))
	if err != nil || contentType != "application/json" {
		return fmt.Errorf("public query stub response must be JSON")
	}
	sum := sha256.Sum256(s.Body)
	if s.ContentHash != "sha256:"+hex.EncodeToString(sum[:]) {
		return fmt.Errorf("public query stub response hash mismatch")
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
	FulfilledPublicQueries         int      `json:"fulfilled_public_queries,omitempty"`
	FulfilledPublicQueryHashes     []string `json:"fulfilled_public_query_hashes,omitempty"`
}

func (a Attestation) Validate(request SessionRequest) error {
	if a.DocumentNavigations < 1 || a.DocumentNavigations > request.Plan.MaxNavigations ||
		a.AllowedWriteRequests != 0 || a.BlockedWriteRequests < 0 || a.CrossOriginDocumentNavigations != 0 || a.FormSubmissions != 0 ||
		a.Downloads != 0 || a.Popups != 0 || !a.PublicEndpoint || !a.RobotsAllowed ||
		a.TermsPolicyVersion != request.Policy.TermsPolicyVersion ||
		(request.ProfileRef != "" && !a.ProfileLeaseAuthorized) {
		return fmt.Errorf("browser broker violated effect policy")
	}
	for _, method := range a.ObservedMethods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
			return fmt.Errorf("browser broker observed unsafe method %q", method)
		}
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
	if a.FulfilledPublicQueries != len(a.FulfilledPublicQueryHashes) || a.FulfilledPublicQueries > len(request.PublicQueryStubs) {
		return fmt.Errorf("browser broker returned inconsistent public query stub evidence")
	}
	seenStubHashes := make(map[string]struct{}, len(a.FulfilledPublicQueryHashes))
	for _, value := range a.FulfilledPublicQueryHashes {
		encoded := strings.TrimPrefix(value, "sha256:")
		decoded, err := hex.DecodeString(encoded)
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("browser broker returned invalid public query stub hash")
		}
		if _, duplicate := seenStubHashes[value]; duplicate {
			return fmt.Errorf("browser broker returned duplicate public query stub hash")
		}
		seenStubHashes[value] = struct{}{}
	}
	return nil
}

type SessionResult struct {
	FinalURL            string                             `json:"final_url"`
	ContentType         string                             `json:"content_type"`
	DOM                 []byte                             `json:"-"`
	Attestation         Attestation                        `json:"attestation"`
	PublicQueryEvidence []recipeabi.PublicQueryObservation `json:"public_query_evidence,omitempty"`
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
	Kind        string      `json:"kind"`
	AttemptID   string      `json:"attempt_id"`
	URL         string      `json:"url"`
	ContentType string      `json:"content_type,omitempty"`
	ContentHash string      `json:"content_hash"`
	Attestation Attestation `json:"attestation"`
	Body        []byte      `json:"-"`
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
	Document    recipeexec.DocumentResult
	Artifact    recipeabi.ArtifactRef
	FinalURL    string
	Attestation Attestation
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
	if spec.Transport != recipeabi.TransportBrowser {
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
	offlineSpec.Transport = recipeabi.TransportHTTPHTML
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
	tooLarge := int64(len(body)) > plan.MaxDOMBytes || int64(len(body)) > spec.Request.MaxResponseBytes
	limit := plan.MaxDOMBytes
	if spec.Request.MaxResponseBytes < limit {
		limit = spec.Request.MaxResponseBytes
	}
	if tooLarge && int64(len(body)) > limit {
		body = body[:limit]
	}
	sum := sha256.Sum256(body)
	contentHash := "sha256:" + hex.EncodeToString(sum[:])
	artifactURL := session.FinalURL
	if strings.TrimSpace(artifactURL) == "" {
		artifactURL = input.Endpoint.URL
	}
	artifact, artifactErr := sink.Put(ctx, ArtifactWrite{Kind: "browser_dom", AttemptID: input.Attempt.AttemptID,
		URL: artifactURL, ContentType: session.ContentType, ContentHash: contentHash, Attestation: session.Attestation, Body: body})
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
	document, err := recipeexec.ExecuteHTML(offlineSpec, body)
	if err != nil {
		return result, &RunError{Class: "parse_error", Cause: err}
	}
	result.Document = document
	return result, nil
}
