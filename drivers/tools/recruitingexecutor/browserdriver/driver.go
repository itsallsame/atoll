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
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeexec"
)

const PlanVersion = "recruiting.browser-plan.v1"

type ActionKind string

const (
	ActionWaitSelector ActionKind = "wait_selector"
	ActionScrollPage   ActionKind = "scroll_page"
	ActionFollowLink   ActionKind = "follow_link"
)

// Action intentionally has no script, input text, form submit, or generic
// click primitive. follow_link means reading an anchor href and navigating
// with GET after the broker verifies the destination origin.
type Action struct {
	Kind       ActionKind `json:"kind"`
	Selector   string     `json:"selector,omitempty"`
	TimeoutMS  int        `json:"timeout_ms,omitempty"`
	MaxRepeats int        `json:"max_repeats,omitempty"`
}

type Plan struct {
	Version        string   `json:"version"`
	Actions        []Action `json:"actions"`
	MaxNavigations int      `json:"max_navigations"`
	MaxDOMBytes    int64    `json:"max_dom_bytes"`
}

func (p Plan) Validate() error {
	if p.Version != PlanVersion || len(p.Actions) > 100 || p.MaxNavigations < 1 || p.MaxNavigations > 100 ||
		p.MaxDOMBytes < 1 || p.MaxDOMBytes > 20<<20 {
		return fmt.Errorf("browser plan requires supported version and bounded actions/navigation/DOM")
	}
	for index, action := range p.Actions {
		if action.TimeoutMS < 0 || action.TimeoutMS > 30_000 || action.MaxRepeats < 0 || action.MaxRepeats > 100 {
			return fmt.Errorf("browser action %d exceeds time or repeat bounds", index)
		}
		switch action.Kind {
		case ActionWaitSelector, ActionFollowLink:
			if strings.TrimSpace(action.Selector) == "" {
				return fmt.Errorf("browser action %d requires selector", index)
			}
			if _, err := cascadia.Parse(action.Selector); err != nil {
				return fmt.Errorf("browser action %d selector: %w", index, err)
			}
		case ActionScrollPage:
			if action.Selector != "" {
				return fmt.Errorf("scroll_page cannot target an interactive element")
			}
		default:
			return fmt.Errorf("browser action %d has unsupported kind %q", index, action.Kind)
		}
	}
	return nil
}

func (p Plan) ContentHash() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type SessionRequest struct {
	EndpointURL    string         `json:"endpoint_url"`
	UserAgent      string         `json:"user_agent"`
	AcceptLanguage string         `json:"accept_language,omitempty"`
	ProfileRef     string         `json:"profile_ref,omitempty"`
	Plan           Plan           `json:"plan"`
	PlanHash       string         `json:"plan_hash"`
	AttemptID      string         `json:"attempt_id"`
	TimeoutMS      int            `json:"timeout_ms"`
	Policy         PolicyEvidence `json:"policy"`
	AllowedMethods []string       `json:"allowed_methods"`
	SameOriginDocs bool           `json:"same_origin_documents"`
	BlockDownloads bool           `json:"block_downloads"`
	BlockPopups    bool           `json:"block_popups"`
}

type Attestation struct {
	DocumentNavigations            int      `json:"document_navigations"`
	ObservedMethods                []string `json:"observed_methods"`
	AllowedWriteRequests           int      `json:"allowed_write_requests"`
	CrossOriginDocumentNavigations int      `json:"cross_origin_document_navigations"`
	FormSubmissions                int      `json:"form_submissions"`
	Downloads                      int      `json:"downloads"`
	Popups                         int      `json:"popups"`
	PublicEndpoint                 bool     `json:"public_endpoint"`
	RobotsAllowed                  bool     `json:"robots_allowed"`
	TermsPolicyVersion             uint64   `json:"terms_policy_version"`
	ProfileLeaseAuthorized         bool     `json:"profile_lease_authorized"`
}

func (a Attestation) Validate(request SessionRequest) error {
	if a.DocumentNavigations < 1 || a.DocumentNavigations > request.Plan.MaxNavigations ||
		a.AllowedWriteRequests != 0 || a.CrossOriginDocumentNavigations != 0 || a.FormSubmissions != 0 ||
		a.Downloads != 0 || a.Popups != 0 || !a.PublicEndpoint || !a.RobotsAllowed ||
		a.TermsPolicyVersion != request.Policy.TermsPolicyVersion ||
		(request.ProfileRef != "" && !a.ProfileLeaseAuthorized) {
		return fmt.Errorf("browser broker violated effect policy")
	}
	for _, method := range a.ObservedMethods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method != http.MethodGet && method != http.MethodHead {
			return fmt.Errorf("browser broker observed unsafe method %q", method)
		}
	}
	return nil
}

type SessionResult struct {
	FinalURL    string      `json:"final_url"`
	ContentType string      `json:"content_type"`
	DOM         []byte      `json:"-"`
	Attestation Attestation `json:"attestation"`
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
	Document recipeexec.DocumentResult
	Artifact recipeabi.ArtifactRef
}

type RunError struct {
	Class string
	Cause error
}

func (e *RunError) Error() string { return fmt.Sprintf("browser driver %s: %v", e.Class, e.Cause) }
func (e *RunError) Unwrap() error { return e.Cause }

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
	offlineSpec := spec
	offlineSpec.Transport = recipeabi.TransportHTTPHTML
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
		Plan: plan, PlanHash: planHash, AttemptID: input.Attempt.AttemptID,
		TimeoutMS:      spec.Request.TimeoutMS,
		Policy:         policy,
		AllowedMethods: []string{http.MethodGet, http.MethodHead}, SameOriginDocs: true, BlockDownloads: true, BlockPopups: true}
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
	artifact, artifactErr := sink.Put(ctx, ArtifactWrite{Kind: "browser_dom", AttemptID: input.Attempt.AttemptID,
		URL: session.FinalURL, ContentType: session.ContentType, ContentHash: contentHash, Attestation: session.Attestation, Body: body})
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
	result := PageResult{Artifact: artifact}
	if brokerErr != nil {
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
