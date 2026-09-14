// Package recipeabi defines the versioned, deterministic contract between the
// recruiting control plane, executor, and site recipes. It is application ABI,
// not an addition to Atoll's generic message protocol.
package recipeabi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
)

const (
	Version                     = "recruiting.recipe.v1"
	MaxSpecBytes                = 256 << 10
	MaxPublicQueryEvidenceBytes = 64 << 10
)

type Kind string

const (
	KindListing   Kind = "listing"
	KindDetail    Kind = "detail"
	KindDiscovery Kind = "discovery"
)

type Transport string

const (
	TransportHTTPJSON Transport = "http_json"
	TransportHTTPHTML Transport = "http_html"
	TransportBrowser  Transport = "browser"
)

type RunInput struct {
	ABIVersion string         `json:"abi_version"`
	Target     TargetRef      `json:"target"`
	Endpoint   EndpointRef    `json:"endpoint"`
	Assignment AssignmentRef  `json:"assignment"`
	Recipe     *RecipeRef     `json:"recipe,omitempty"`
	Backfill   *BackfillRef   `json:"backfill,omitempty"`
	Checkpoint *CheckpointRef `json:"checkpoint,omitempty"`
	ProfileRef string         `json:"profile_ref,omitempty"`
	Budget     BudgetRef      `json:"budget"`
	Attempt    AttemptFence   `json:"attempt"`
}

type TargetRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type EndpointRef struct {
	URL     string `json:"url"`
	Version uint64 `json:"version"`
}

type AssignmentRef struct {
	RecipeID          string `json:"recipe_id"`
	RecipeVersion     uint64 `json:"recipe_version"`
	AssignmentVersion uint64 `json:"assignment_version"`
	ContractHash      string `json:"contract_hash"`
}

type RecipeRef struct {
	RecipeID      string `json:"recipe_id"`
	RecipeVersion uint64 `json:"recipe_version"`
	ContractHash  string `json:"contract_hash"`
}

type BackfillRef struct {
	BackfillID string `json:"backfill_id"`
	ItemID     string `json:"item_id"`
	Mode       string `json:"mode"`
}

type CheckpointRef struct {
	Version        uint64   `json:"version"`
	FrontierKeys   []string `json:"frontier_keys,omitempty"`
	LastActivityAt string   `json:"last_activity_at,omitempty"`
}

type BudgetRef struct {
	PermitID      string `json:"permit_id"`
	PolicyVersion uint64 `json:"policy_version"`
	WorkloadClass string `json:"workload_class,omitempty"`
}

type AttemptFence struct {
	WorkID              string `json:"work_id"`
	AttemptID           string `json:"attempt_id"`
	AcceptanceVersion   uint64 `json:"acceptance_version"`
	CompanyVersion      uint64 `json:"company_version"`
	SourceVersion       uint64 `json:"source_version"`
	ProfileVersion      uint64 `json:"profile_version,omitempty"`
	DiscoveryGeneration uint64 `json:"discovery_generation,omitempty"`
	RecipeValidation    bool   `json:"recipe_validation,omitempty"`
}

func (in RunInput) Validate() error {
	if in.ABIVersion != Version {
		return fmt.Errorf("unsupported recipe ABI %q", in.ABIVersion)
	}
	if blank(in.Target.Kind, in.Target.ID) || in.Endpoint.Version == 0 || blank(in.Budget.PermitID) || in.Budget.PolicyVersion == 0 ||
		blank(in.Attempt.WorkID, in.Attempt.AttemptID) || in.Attempt.AcceptanceVersion == 0 || in.Attempt.CompanyVersion == 0 {
		return fmt.Errorf("complete target, endpoint, budget, and attempt fence are required")
	}
	if in.Backfill != nil {
		if in.Target.Kind != "job" || blank(in.Backfill.BackfillID, in.Backfill.ItemID, in.Backfill.Mode) ||
			(in.Backfill.Mode != "artifact_recompute" && in.Backfill.Mode != "live_refetch") ||
			in.Recipe == nil || blank(in.Recipe.RecipeID, in.Recipe.ContractHash) || in.Recipe.RecipeVersion == 0 ||
			in.Assignment != (AssignmentRef{}) || in.Checkpoint != nil || in.Attempt.SourceVersion == 0 ||
			in.Attempt.DiscoveryGeneration != 0 || in.Attempt.RecipeValidation {
			return fmt.Errorf("backfill job input requires explicit Recipe and backfill lineage without Assignment or Checkpoint")
		}
	} else if in.Target.Kind == "company" {
		if in.Recipe == nil || blank(in.Recipe.RecipeID, in.Recipe.ContractHash) || in.Recipe.RecipeVersion == 0 ||
			(in.Attempt.DiscoveryGeneration == 0) != in.Attempt.RecipeValidation || in.Attempt.SourceVersion != 0 ||
			in.Assignment != (AssignmentRef{}) || in.Checkpoint != nil {
			return fmt.Errorf("company discovery input requires either a production generation or Recipe validation without Source or Assignment")
		}
	} else if in.Recipe != nil || blank(in.Assignment.RecipeID, in.Assignment.ContractHash) ||
		in.Assignment.RecipeVersion == 0 || in.Assignment.AssignmentVersion == 0 || in.Attempt.SourceVersion == 0 ||
		in.Attempt.DiscoveryGeneration != 0 || in.Attempt.RecipeValidation {
		return fmt.Errorf("source and job inputs require Assignment and Source fence without discovery Recipe")
	}
	switch in.Budget.WorkloadClass {
	case "", "baseline", "calibration", "backfill":
	default:
		return fmt.Errorf("unsupported execution workload class %q", in.Budget.WorkloadClass)
	}
	endpoint, err := url.Parse(in.Endpoint.URL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return fmt.Errorf("endpoint must be an absolute http(s) URL without credentials or fragment")
	}
	if in.ProfileRef != "" {
		profile, profileErr := url.Parse(in.ProfileRef)
		if profileErr != nil || profile.Scheme != "profile" || profile.Host == "" || profile.User != nil || profile.RawQuery != "" || profile.Fragment != "" {
			return fmt.Errorf("profile_ref must be an opaque profile:// reference")
		}
	}
	if in.ProfileRef == "" && in.Attempt.ProfileVersion != 0 {
		return fmt.Errorf("profile version requires a profile reference")
	}
	if in.Checkpoint != nil {
		if in.Checkpoint.Version == 0 || len(in.Checkpoint.FrontierKeys) > 100 {
			return fmt.Errorf("checkpoint version must be positive and frontier bounded")
		}
		seen := make(map[string]struct{}, len(in.Checkpoint.FrontierKeys))
		for _, key := range in.Checkpoint.FrontierKeys {
			key = strings.TrimSpace(key)
			if key == "" {
				return fmt.Errorf("checkpoint frontier keys must be non-empty")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("checkpoint frontier keys must be unique")
			}
			seen[key] = struct{}{}
		}
		if in.Checkpoint.LastActivityAt != "" {
			if _, err := time.Parse(time.RFC3339, in.Checkpoint.LastActivityAt); err != nil {
				return fmt.Errorf("checkpoint last_activity_at must be RFC3339")
			}
		}
	}
	return nil
}

type Spec struct {
	ABIVersion         string            `json:"abi_version"`
	Kind               Kind              `json:"kind"`
	RequiredCapability string            `json:"required_capability"`
	Transport          Transport         `json:"transport"`
	Request            ReadRequest       `json:"request"`
	Extraction         Extraction        `json:"extraction"`
	OffsetPagination   *OffsetPagination `json:"offset_pagination,omitempty"`
	Listing            *ListingContract  `json:"listing,omitempty"`
	BrowserPlan        *BrowserPlan      `json:"browser_plan,omitempty"`
}

const BrowserPlanVersion = "recruiting.browser-plan.v1"

type BrowserActionKind string

const (
	BrowserActionWaitSelector BrowserActionKind = "wait_selector"
	BrowserActionScrollPage   BrowserActionKind = "scroll_page"
	BrowserActionFollowLink   BrowserActionKind = "follow_link"
)

// BrowserAction intentionally has no script, input, form submit, or generic
// click primitive. FollowLink reads an anchor href and navigates with GET only
// after the broker verifies that the destination remains same-origin.
type BrowserAction struct {
	Kind       BrowserActionKind `json:"kind"`
	Selector   string            `json:"selector,omitempty"`
	TimeoutMS  int               `json:"timeout_ms,omitempty"`
	MaxRepeats int               `json:"max_repeats,omitempty"`
}

type BrowserPlan struct {
	Version              string          `json:"version"`
	Actions              []BrowserAction `json:"actions"`
	AuthExpiredSelectors []string        `json:"auth_expired_selectors,omitempty"`
	CaptchaSelectors     []string        `json:"captcha_selectors,omitempty"`
	MaxNavigations       int             `json:"max_navigations"`
	MaxDOMBytes          int64           `json:"max_dom_bytes"`
}

func (p BrowserPlan) Validate() error {
	if p.Version != BrowserPlanVersion || len(p.Actions) > 100 || p.MaxNavigations < 1 || p.MaxNavigations > 100 ||
		p.MaxDOMBytes < 1 || p.MaxDOMBytes > 20<<20 || len(p.AuthExpiredSelectors) > 10 || len(p.CaptchaSelectors) > 10 {
		return fmt.Errorf("browser plan requires supported version and bounded actions/navigation/DOM")
	}
	failureSelectors := make(map[string]string, len(p.AuthExpiredSelectors)+len(p.CaptchaSelectors))
	for class, selectors := range map[string][]string{
		"auth_expired": p.AuthExpiredSelectors,
		"captcha":      p.CaptchaSelectors,
	} {
		for index, selector := range selectors {
			if selector == "" || selector != strings.TrimSpace(selector) {
				return fmt.Errorf("browser %s selector %d must be non-empty and trimmed", class, index)
			}
			if _, err := cascadia.Parse(selector); err != nil {
				return fmt.Errorf("browser %s selector %d: %w", class, index, err)
			}
			if previous, duplicate := failureSelectors[selector]; duplicate {
				return fmt.Errorf("browser failure selector %q is ambiguous between %s and %s", selector, previous, class)
			}
			failureSelectors[selector] = class
		}
	}
	navigations := 1
	for index, action := range p.Actions {
		if action.TimeoutMS < 0 || action.TimeoutMS > 30_000 || action.MaxRepeats < 0 || action.MaxRepeats > 100 {
			return fmt.Errorf("browser action %d exceeds time or repeat bounds", index)
		}
		switch action.Kind {
		case BrowserActionWaitSelector, BrowserActionFollowLink:
			if strings.TrimSpace(action.Selector) == "" {
				return fmt.Errorf("browser action %d requires selector", index)
			}
			if _, err := cascadia.Parse(action.Selector); err != nil {
				return fmt.Errorf("browser action %d selector: %w", index, err)
			}
			if action.MaxRepeats != 0 || (action.Kind == BrowserActionFollowLink && action.TimeoutMS != 0) {
				return fmt.Errorf("browser action %d declares parameters unused by %q", index, action.Kind)
			}
			if action.Kind == BrowserActionFollowLink {
				navigations++
			}
		case BrowserActionScrollPage:
			if action.Selector != "" || action.TimeoutMS != 0 || action.MaxRepeats < 1 {
				return fmt.Errorf("scroll_page requires a positive bounded repeat count without selector or timeout")
			}
		default:
			return fmt.Errorf("browser action %d has unsupported kind %q", index, action.Kind)
		}
	}
	if navigations > p.MaxNavigations {
		return fmt.Errorf("browser plan declares %d navigations above its limit of %d", navigations, p.MaxNavigations)
	}
	return nil
}

func (p BrowserPlan) ContentHash() (string, error) {
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

type ReadRequest struct {
	Method           string            `json:"method"`
	Headers          map[string]string `json:"headers,omitempty"`
	JSONBody         json.RawMessage   `json:"json_body,omitempty"`
	TimeoutMS        int               `json:"timeout_ms"`
	MaxResponseBytes int64             `json:"max_response_bytes"`
	MaxRedirects     int               `json:"max_redirects"`
	UserAgent        string            `json:"user_agent"`
}

// PublicQueryObservation is sanitized network evidence from an isolated
// public-browser session. The browser still blocks the POST; this record only
// permits a later, independently validated HTTP Recipe to reproduce a query
// that carries no credentials or arbitrary headers.
type PublicQueryObservation struct {
	EndpointURL string            `json:"endpoint_url"`
	Method      string            `json:"method"`
	Headers     map[string]string `json:"headers"`
	JSONBody    json.RawMessage   `json:"json_body"`
	BodyHash    string            `json:"body_hash"`
}

func (o PublicQueryObservation) EncodedSize() int {
	size := len(o.EndpointURL) + len(o.Method) + len(o.JSONBody) + len(o.BodyHash)
	for name, value := range o.Headers {
		size += len(name) + len(value)
	}
	return size
}

func (o PublicQueryObservation) MatchesObservation(other PublicQueryObservation) bool {
	if o.Validate() != nil || other.Validate() != nil {
		return false
	}
	left, leftErr := o.Canonicalized()
	right, rightErr := other.Canonicalized()
	if leftErr != nil || rightErr != nil || left.EndpointURL != right.EndpointURL || left.Method != right.Method ||
		left.BodyHash != right.BodyHash || len(left.Headers) != len(right.Headers) {
		return false
	}
	return equalPublicHeaders(left.Headers, right.Headers)
}

func NewPublicQueryObservation(endpointURL, method string, headers map[string]string, body json.RawMessage) (PublicQueryObservation, error) {
	observation := PublicQueryObservation{EndpointURL: strings.TrimSpace(endpointURL), Method: strings.ToUpper(strings.TrimSpace(method)),
		Headers: headers, JSONBody: append(json.RawMessage(nil), body...)}
	return observation.Canonicalized()
}

// Canonicalized returns a semantically stable observation. Database JSON
// columns are allowed to reorder object keys and whitespace, so evidence must
// be bound to canonical JSON rather than the original byte representation.
// It also provides a narrow compatibility path for already-attested browser
// evidence that was persisted with the former raw-byte hash.
func (o PublicQueryObservation) Canonicalized() (PublicQueryObservation, error) {
	canonicalBody, err := canonicalJSONObject(o.JSONBody)
	if err != nil {
		return PublicQueryObservation{}, fmt.Errorf("public-query observation JSON: %w", err)
	}
	o.JSONBody = canonicalBody
	sum := sha256.Sum256(canonicalBody)
	o.BodyHash = "sha256:" + hex.EncodeToString(sum[:])
	if err := o.Validate(); err != nil {
		return PublicQueryObservation{}, err
	}
	return o, nil
}

func (o PublicQueryObservation) Validate() error {
	endpoint, err := url.Parse(o.EndpointURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.Fragment != "" || o.Method != "POST" || len(o.JSONBody) == 0 || len(o.JSONBody) > 64<<10 {
		return fmt.Errorf("public-query observation requires an absolute HTTP(S) POST with bounded JSON")
	}
	for name := range endpoint.Query() {
		lower := strings.ToLower(strings.TrimSpace(name))
		if strings.Contains(lower, "signature") || strings.Contains(lower, "token") ||
			strings.Contains(lower, "secret") || strings.Contains(lower, "password") ||
			strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") ||
			strings.Contains(lower, "csrf") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") {
			return fmt.Errorf("public-query observation URL contains an ephemeral or sensitive query parameter")
		}
	}
	body, err := decodeUniqueJSONObject(o.JSONBody)
	if err != nil {
		return fmt.Errorf("public-query observation JSON: %w", err)
	}
	if err := validatePublicQueryValues(body, 0); err != nil {
		return err
	}
	if err := validatePublicQueryHeaders(o.Headers); err != nil {
		return err
	}
	if !strings.EqualFold(recipeHeader(o.Headers, "content-type"), "application/json") {
		return fmt.Errorf("public-query observation requires JSON Content-Type")
	}
	canonicalBody, err := canonicalJSONObject(o.JSONBody)
	if err != nil {
		return fmt.Errorf("public-query observation JSON: %w", err)
	}
	sum := sha256.Sum256(canonicalBody)
	if o.BodyHash != "sha256:"+hex.EncodeToString(sum[:]) {
		return fmt.Errorf("public-query observation body hash does not match")
	}
	return nil
}

func (o PublicQueryObservation) MatchesReadRequest(request ReadRequest) bool {
	if o.Validate() != nil {
		return false
	}
	canonical, err := o.Canonicalized()
	if err != nil || strings.ToUpper(strings.TrimSpace(request.Method)) != canonical.Method {
		return false
	}
	requestBody, err := canonicalJSONObject(request.JSONBody)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(requestBody)
	return canonical.BodyHash == "sha256:"+hex.EncodeToString(sum[:]) && equalPublicHeaders(canonical.Headers, request.Headers)
}

type Extraction struct {
	Collection     string            `json:"collection,omitempty"`
	CollectionRoot bool              `json:"collection_root,omitempty"`
	Fields         map[string]string `json:"fields"`
	Templates      map[string]string `json:"templates,omitempty"`
	Attributes     map[string]string `json:"attributes,omitempty"`
	Next           string            `json:"next,omitempty"`
	NextAttribute  string            `json:"next_attribute,omitempty"`
}

// OffsetPagination describes APIs that paginate through a same-origin offset
// query instead of embedding a next URL. Metadata mode reads offset, limit,
// and total from the response. Short-page mode pins a page size and stops when
// the response collection is shorter; both modes remain bounded by Listing.
type OffsetPagination struct {
	OffsetPointer   string `json:"offset_pointer,omitempty"`
	LimitPointer    string `json:"limit_pointer,omitempty"`
	TotalPointer    string `json:"total_pointer,omitempty"`
	OffsetQuery     string `json:"offset_query,omitempty"`
	LimitQuery      string `json:"limit_query,omitempty"`
	OffsetBodyField string `json:"offset_body_field,omitempty"`
	LimitBodyField  string `json:"limit_body_field,omitempty"`
	PageSize        int    `json:"page_size,omitempty"`
}

type ListingContract struct {
	IdentityField      string `json:"identity_field"`
	DetailURLField     string `json:"detail_url_field"`
	ActivityField      string `json:"activity_field,omitempty"`
	ActivityTimeFormat string `json:"activity_time_format,omitempty"`
	BoundaryMode       string `json:"boundary_mode"`
	Ordering           string `json:"ordering"`
	UpdateRetop        bool   `json:"update_retop"`
	OverlapPages       int    `json:"overlap_pages"`
	MaxPages           int    `json:"max_pages"`
	MaxItemsPerPage    int    `json:"max_items_per_page"`
	MaxTotalBytes      int64  `json:"max_total_bytes"`
	FrontierWidth      int    `json:"frontier_width"`
	ExcludePinnedField string `json:"exclude_pinned_field,omitempty"`
}

func (s Spec) Validate() error {
	if s.ABIVersion != Version || blank(string(s.Kind), s.RequiredCapability) {
		return fmt.Errorf("recipe ABI, kind, and required capability are required")
	}
	switch s.Kind {
	case KindListing, KindDetail, KindDiscovery:
	default:
		return fmt.Errorf("unsupported recipe kind %q", s.Kind)
	}
	switch s.Transport {
	case TransportHTTPJSON, TransportHTTPHTML, TransportBrowser:
	default:
		return fmt.Errorf("unsupported recipe transport %q", s.Transport)
	}
	method := strings.ToUpper(strings.TrimSpace(s.Request.Method))
	if method != "GET" && method != "POST" {
		return fmt.Errorf("recipe request must be GET or a constrained public-query POST")
	}
	if method == "GET" && len(s.Request.JSONBody) != 0 {
		return fmt.Errorf("GET Recipe cannot declare a JSON body")
	}
	if method == "POST" {
		body, bodyErr := decodeUniqueJSONObject(s.Request.JSONBody)
		if s.Transport != TransportHTTPJSON || s.Request.MaxRedirects != 0 || len(s.Request.JSONBody) == 0 ||
			len(s.Request.JSONBody) > 64<<10 || bodyErr != nil {
			return fmt.Errorf("public-query POST requires HTTP JSON, a bounded JSON object, and zero redirects")
		}
		if err := validatePublicQueryValues(body, 0); err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(recipeHeader(s.Request.Headers, "content-type")), "application/json") {
			return fmt.Errorf("public-query POST requires Content-Type application/json")
		}
	}
	if s.Transport == TransportBrowser && method != "GET" {
		return fmt.Errorf("browser Recipe document navigation remains GET-only")
	}
	if s.Request.TimeoutMS < 100 || s.Request.TimeoutMS > 60_000 || s.Request.MaxResponseBytes < 1 || s.Request.MaxResponseBytes > 20<<20 ||
		s.Request.MaxRedirects < 0 || s.Request.MaxRedirects > 5 || strings.TrimSpace(s.Request.UserAgent) == "" {
		return fmt.Errorf("request budgets exceed the recipe ABI limits")
	}
	if err := validatePublicQueryHeaders(s.Request.Headers); err != nil {
		return err
	}
	if method == "GET" && recipeHeader(s.Request.Headers, "content-type") != "" {
		return fmt.Errorf("GET Recipe cannot declare Content-Type")
	}
	if strings.ContainsAny(s.Request.UserAgent, "\r\n") {
		return fmt.Errorf("recipe user agent cannot contain control newlines")
	}
	if len(s.Extraction.Fields) == 0 {
		return fmt.Errorf("at least one extraction field is required")
	}
	for name, expression := range s.Extraction.Fields {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(expression) == "" {
			return fmt.Errorf("extraction fields require non-empty names and expressions")
		}
	}
	if s.Transport == TransportHTTPJSON {
		if len(s.Extraction.Attributes) != 0 || s.Extraction.NextAttribute != "" {
			return fmt.Errorf("JSON extraction cannot declare HTML attributes")
		}
		for _, pointer := range s.Extraction.Fields {
			if !strings.HasPrefix(pointer, "/") {
				return fmt.Errorf("JSON extraction fields require JSON pointers")
			}
		}
		for field, template := range s.Extraction.Templates {
			if s.Kind != KindListing || s.Listing == nil || field != s.Listing.DetailURLField ||
				s.Extraction.Fields[field] == "" || strings.Count(template, "{value}") != 1 || len(template) > 2048 {
				return fmt.Errorf("JSON templates are restricted to one bounded listing detail URL template")
			}
			probe, err := url.Parse(strings.Replace(template, "{value}", "probe", 1))
			if err != nil || (probe.Scheme != "http" && probe.Scheme != "https") || probe.Host == "" ||
				probe.User != nil || probe.Fragment != "" {
				return fmt.Errorf("listing detail URL template must produce an absolute public HTTP(S) URL")
			}
		}
		for _, pointer := range []string{s.Extraction.Collection, s.Extraction.Next} {
			if pointer != "" && !strings.HasPrefix(pointer, "/") {
				return fmt.Errorf("JSON collection and next expressions must be JSON pointers")
			}
		}
	} else if s.Extraction.CollectionRoot {
		return fmt.Errorf("collection_root is only valid for JSON extraction")
	}
	if s.OffsetPagination != nil {
		page := s.OffsetPagination
		queryMode := safeQueryName(page.OffsetQuery) && page.OffsetBodyField == "" && page.LimitBodyField == ""
		bodyMode := safeJSONField(page.OffsetBodyField) && page.OffsetQuery == "" && page.LimitQuery == "" &&
			method == "POST"
		metadataMode := jsonPointer(page.OffsetPointer) && jsonPointer(page.LimitPointer) && jsonPointer(page.TotalPointer) &&
			page.LimitQuery == "" && page.LimitBodyField == "" && page.PageSize == 0
		shortPageMode := page.OffsetPointer == "" && page.LimitPointer == "" && page.TotalPointer == "" &&
			((queryMode && safeQueryName(page.LimitQuery)) || (bodyMode && safeJSONField(page.LimitBodyField))) &&
			page.PageSize >= 1 && page.PageSize <= 500
		if s.Kind != KindListing || s.Transport != TransportHTTPJSON || s.Extraction.Next != "" ||
			(!queryMode && !bodyMode) || (!metadataMode && !shortPageMode) {
			return fmt.Errorf("offset pagination requires a JSON listing, one safe query/body offset location, metadata pointers or bounded short-page mode, and no next expression")
		}
		if bodyMode {
			body, bodyErr := decodeUniqueJSONObject(s.Request.JSONBody)
			if bodyErr != nil {
				return fmt.Errorf("offset body pagination requires a JSON object")
			}
			if _, ok := nonNegativeJSONInteger(body[page.OffsetBodyField]); !ok {
				return fmt.Errorf("offset body field must contain a non-negative integer")
			}
			if page.LimitBodyField != "" {
				limit, ok := nonNegativeJSONInteger(body[page.LimitBodyField])
				if !ok || int(limit) != page.PageSize {
					return fmt.Errorf("limit body field must equal the configured page size")
				}
			}
		}
	}
	if s.Transport == TransportHTTPHTML || s.Transport == TransportBrowser {
		for field, attribute := range s.Extraction.Attributes {
			if _, ok := s.Extraction.Fields[field]; !ok || !safeAttributeName(attribute) {
				return fmt.Errorf("HTML attributes must reference extracted fields and use safe names")
			}
		}
		if s.Extraction.NextAttribute != "" && !safeAttributeName(s.Extraction.NextAttribute) {
			return fmt.Errorf("HTML next_attribute is invalid")
		}
	}
	if s.Transport == TransportBrowser && s.RequiredCapability != "browser.profile.repair" {
		if s.BrowserPlan == nil {
			return fmt.Errorf("browser recipe requires a constrained browser plan")
		}
		if err := s.BrowserPlan.Validate(); err != nil {
			return err
		}
		if s.Extraction.Next != "" || s.Extraction.NextAttribute != "" || s.OffsetPagination != nil {
			return fmt.Errorf("browser recipe plan must produce one terminal DOM without a second pagination protocol")
		}
	} else if s.BrowserPlan != nil && s.Transport != TransportBrowser {
		return fmt.Errorf("non-browser recipe cannot carry a browser plan")
	}
	if s.Kind == KindListing {
		if err := s.Listing.Validate(); err != nil {
			return err
		}
		if _, ok := s.Extraction.Fields[s.Listing.IdentityField]; !ok {
			return fmt.Errorf("listing identity_field must name an extracted field")
		}
		if _, ok := s.Extraction.Fields[s.Listing.DetailURLField]; !ok {
			return fmt.Errorf("listing detail_url_field must name an extracted field")
		}
		if s.Transport == TransportHTTPJSON && s.Extraction.CollectionRoot == (s.Extraction.Collection != "") {
			return fmt.Errorf("JSON listing extraction requires exactly one collection pointer or collection_root")
		}
		if s.Listing.ActivityField != "" {
			if _, ok := s.Extraction.Fields[s.Listing.ActivityField]; !ok {
				return fmt.Errorf("listing activity_field must name an extracted field")
			}
		}
		if s.Listing.ExcludePinnedField != "" {
			if _, ok := s.Extraction.Fields[s.Listing.ExcludePinnedField]; !ok {
				return fmt.Errorf("listing exclude_pinned_field must name an extracted field")
			}
		}
	} else if s.Listing != nil {
		return fmt.Errorf("listing contract is only valid for listing recipes")
	}
	if s.Kind == KindDiscovery {
		if s.Transport == TransportHTTPJSON && s.Extraction.CollectionRoot == (s.Extraction.Collection != "") {
			return fmt.Errorf("JSON discovery extraction requires exactly one collection pointer or collection_root")
		}
		if strings.TrimSpace(s.Extraction.Collection) == "" && !s.Extraction.CollectionRoot {
			return fmt.Errorf("discovery recipe requires a bounded candidate collection")
		}
		if _, ok := s.Extraction.Fields["endpoint"]; !ok {
			return fmt.Errorf("discovery recipe requires an endpoint extraction field")
		}
		if _, ok := s.Extraction.Fields["confidence_basis"]; !ok {
			return fmt.Errorf("discovery recipe requires a confidence_basis extraction field")
		}
	}
	return nil
}

func (c *ListingContract) Validate() error {
	if c == nil || strings.TrimSpace(c.IdentityField) == "" || strings.TrimSpace(c.DetailURLField) == "" || c.Ordering != "newest_activity_desc" || !c.UpdateRetop ||
		c.OverlapPages < 1 || c.OverlapPages > 20 || c.MaxPages < c.OverlapPages || c.MaxPages > 1000 ||
		c.MaxItemsPerPage < 1 || c.MaxItemsPerPage > 500 || c.MaxTotalBytes < 1 || c.MaxTotalBytes > 1<<30 {
		return fmt.Errorf("listing recipe requires identity, descending activity/update-retop contract, and bounded overlap/pages")
	}
	if c.FrontierWidth < 1 || c.FrontierWidth > 100 {
		return fmt.Errorf("listing frontier width must be in [1,100]")
	}
	switch c.ActivityTimeFormat {
	case "", "rfc3339", "utc_datetime":
	default:
		return fmt.Errorf("unsupported listing activity_time_format %q", c.ActivityTimeFormat)
	}
	if c.ActivityTimeFormat != "" && strings.TrimSpace(c.ActivityField) == "" {
		return fmt.Errorf("listing activity_time_format requires activity_field")
	}
	switch c.BoundaryMode {
	case "activity_time":
		if strings.TrimSpace(c.ActivityField) == "" {
			return fmt.Errorf("activity_time boundary requires activity_field")
		}
	case "frontier_keys":
	default:
		return fmt.Errorf("unsupported listing boundary mode %q", c.BoundaryMode)
	}
	return nil
}

func safeAttributeName(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "href" || value == "datetime" || value == "content" || value == "value" || strings.HasPrefix(value, "data-") {
		return len(value) <= 64
	}
	return false
}

func jsonPointer(value string) bool {
	return strings.HasPrefix(value, "/") && len(value) <= 512 && !strings.ContainsAny(value, "\r\n")
}

func safeQueryName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || index > 0 && char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func safeJSONField(value string) bool { return safeQueryName(value) }

func validatePublicQueryHeaders(headers map[string]string) error {
	canonicalHeaders := make(map[string]struct{}, len(headers))
	for name, value := range headers {
		canonicalName := strings.ToLower(strings.TrimSpace(name))
		if _, duplicate := canonicalHeaders[canonicalName]; duplicate {
			return fmt.Errorf("recipe header %q is duplicated case-insensitively", name)
		}
		canonicalHeaders[canonicalName] = struct{}{}
		switch canonicalName {
		case "accept", "accept-language", "content-type", "website-path":
		default:
			return fmt.Errorf("recipe header %q is not allowed", name)
		}
		if len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(name+value, "\r\n") {
			return fmt.Errorf("recipe headers cannot contain control newlines")
		}
	}
	return nil
}

func equalPublicHeaders(left, right map[string]string) bool {
	normalize := func(values map[string]string) map[string]string {
		result := make(map[string]string, len(values))
		for name, value := range values {
			result[strings.ToLower(strings.TrimSpace(name))] = value
		}
		return result
	}
	a, b := normalize(left), normalize(right)
	if len(a) != len(b) {
		return false
	}
	for name, value := range a {
		if b[name] != value {
			return false
		}
	}
	return true
}

func validatePublicQueryValues(object map[string]json.RawMessage, depth int) error {
	if depth > 6 || len(object) > 100 {
		return fmt.Errorf("public-query JSON structure exceeds its bound")
	}
	for name, raw := range object {
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == "" || len(name) > 128 || strings.ContainsAny(lower, "\r\n") ||
			strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
			strings.Contains(lower, "password") || strings.Contains(lower, "authorization") ||
			strings.Contains(lower, "signature") || strings.Contains(lower, "cookie") ||
			strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") || strings.Contains(lower, "csrf") {
			return fmt.Errorf("public-query JSON contains a sensitive or invalid field")
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("public-query JSON value is invalid")
		}
		if err := validatePublicQueryValue(value, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validatePublicQueryValue(value any, depth int) error {
	if depth > 6 {
		return fmt.Errorf("public-query JSON nesting exceeds its bound")
	}
	switch typed := value.(type) {
	case nil, bool, json.Number:
		return nil
	case string:
		if len(typed) > 2048 || strings.ContainsAny(typed, "\r\n") {
			return fmt.Errorf("public-query JSON string exceeds its bound")
		}
		return nil
	case []any:
		if len(typed) > 500 {
			return fmt.Errorf("public-query JSON array exceeds its bound")
		}
		for _, item := range typed {
			if err := validatePublicQueryValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		raw := make(map[string]json.RawMessage, len(typed))
		for name, item := range typed {
			encoded, _ := json.Marshal(item)
			raw[name] = encoded
		}
		return validatePublicQueryValues(raw, depth)
	default:
		return fmt.Errorf("public-query JSON contains an unsupported value")
	}
}

func canonicalJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return nil, fmt.Errorf("JSON body exceeds its byte bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeUniqueJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("JSON body must be an object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("JSON body contains trailing data")
		}
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(canonical), nil
}

func decodeUniqueJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 6 {
		return nil, fmt.Errorf("public-query JSON nesting exceeds its bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		switch token.(type) {
		case nil, bool, string, json.Number:
			return token, nil
		default:
			return nil, fmt.Errorf("public-query JSON contains an unsupported value")
		}
	}
	switch delimiter {
	case '{':
		result := make(map[string]any)
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return nil, keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("JSON object key must be a string")
			}
			if _, duplicate := result[key]; duplicate {
				return nil, fmt.Errorf("duplicate JSON object key %q", key)
			}
			value, valueErr := decodeUniqueJSONValue(decoder, depth+1)
			if valueErr != nil {
				return nil, valueErr
			}
			result[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return result, nil
	case '[':
		result := make([]any, 0)
		for decoder.More() {
			value, valueErr := decodeUniqueJSONValue(decoder, depth+1)
			if valueErr != nil {
				return nil, valueErr
			}
			result = append(result, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return result, nil
	default:
		return nil, fmt.Errorf("public-query JSON contains an unsupported delimiter")
	}
}

// decodeUniqueJSONObject rejects duplicate top-level keys. They are otherwise
// silently collapsed by encoding/json, which could make the first POST and
// subsequent pagination requests carry different effective values.
func decodeUniqueJSONObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, fmt.Errorf("JSON body must be an object")
	}
	result := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		if keyErr != nil {
			return nil, keyErr
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("JSON object key must be a string")
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate JSON object key %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("JSON body contains trailing data")
		}
		return nil, err
	}
	return result, nil
}

func recipeHeader(headers map[string]string, requested string) string {
	for name, value := range headers {
		if strings.EqualFold(strings.TrimSpace(name), requested) {
			return value
		}
	}
	return ""
}

func nonNegativeJSONInteger(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var value json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	integer, err := value.Int64()
	return integer, err == nil && integer >= 0
}

func (s Spec) ContentHash() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ContractHash identifies request semantics, extraction, and incremental
// compatibility independently from operational tuning such as timeout,
// response limits, redirects, and User-Agent. A changed public-query filter,
// selector, identity/activity mapping, pagination rule, or listing boundary
// therefore cannot silently reuse an existing Assignment lineage.
func (s Spec) ContractHash() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	contract := struct {
		Kind      Kind      `json:"kind"`
		Transport Transport `json:"transport"`
		Request   struct {
			Method   string            `json:"method"`
			Headers  map[string]string `json:"headers,omitempty"`
			JSONBody json.RawMessage   `json:"json_body,omitempty"`
		} `json:"request"`
		Extraction       Extraction        `json:"extraction"`
		OffsetPagination *OffsetPagination `json:"offset_pagination,omitempty"`
		Listing          *ListingContract  `json:"listing,omitempty"`
		BrowserPlan      *BrowserPlan      `json:"browser_plan,omitempty"`
	}{
		Kind: s.Kind, Transport: s.Transport,
		Request: struct {
			Method   string            `json:"method"`
			Headers  map[string]string `json:"headers,omitempty"`
			JSONBody json.RawMessage   `json:"json_body,omitempty"`
		}{Method: strings.ToUpper(strings.TrimSpace(s.Request.Method)), Headers: s.Request.Headers, JSONBody: s.Request.JSONBody},
		Extraction: s.Extraction, OffsetPagination: s.OffsetPagination, Listing: s.Listing, BrowserPlan: s.BrowserPlan,
	}
	raw, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// DecodeSpec is the shared strict Resource decoder used by both proposal
// ingestion and execution. It prevents the control and data planes from
// accepting different JSON dialects or size limits for the same Recipe ABI.
func DecodeSpec(raw []byte) (Spec, error) {
	if len(raw) == 0 || len(raw) > MaxSpecBytes {
		return Spec{}, fmt.Errorf("recipe resource size must be in [1,%d] bytes", MaxSpecBytes)
	}
	var spec Spec
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("decode recipe resource: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Spec{}, errors.New("decode recipe resource: multiple JSON values")
		}
		return Spec{}, fmt.Errorf("decode recipe resource: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, fmt.Errorf("validate recipe resource: %w", err)
	}
	return spec, nil
}

type ArtifactRef struct {
	ArtifactID  string `json:"artifact_id"`
	ContentHash string `json:"content_hash"`
	ObjectRef   string `json:"object_ref"`
	Kind        string `json:"kind,omitempty"`
}

type QualityProof struct {
	IdentityComplete        bool `json:"identity_complete"`
	OrderingContractHeld    bool `json:"ordering_contract_held"`
	PaginationStable        bool `json:"pagination_stable"`
	PreviousFrontierReached bool `json:"previous_frontier_reached"`
	OverlapCompleted        bool `json:"overlap_completed"`
	ItemCount               int  `json:"item_count"`
}

func (q QualityProof) MayAdvanceCheckpoint() bool {
	return q.IdentityComplete && q.OrderingContractHeld && q.PaginationStable && q.PreviousFrontierReached && q.OverlapCompleted
}

type Failure struct {
	Class       string      `json:"class"`
	Signature   string      `json:"failure_signature,omitempty"`
	Retryable   bool        `json:"retryable"`
	Artifact    ArtifactRef `json:"artifact"`
	NeedsRepair bool        `json:"needs_repair"`
}

type RunOutput struct {
	ABIVersion string          `json:"abi_version"`
	AttemptID  string          `json:"attempt_id"`
	Artifacts  []ArtifactRef   `json:"artifacts"`
	Result     json.RawMessage `json:"result,omitempty"`
	Quality    QualityProof    `json:"quality"`
	Failure    *Failure        `json:"failure,omitempty"`
}

func (out RunOutput) Validate() error {
	if out.ABIVersion != Version || strings.TrimSpace(out.AttemptID) == "" || out.Quality.ItemCount < 0 {
		return fmt.Errorf("output ABI, attempt, and non-negative item count are required")
	}
	for _, artifact := range out.Artifacts {
		if err := artifact.Validate(); err != nil {
			return err
		}
	}
	if out.Failure == nil {
		if len(out.Artifacts) == 0 || len(out.Result) == 0 || !json.Valid(out.Result) {
			return fmt.Errorf("successful output requires artifact evidence and valid JSON result")
		}
		return nil
	}
	if len(out.Result) != 0 {
		return fmt.Errorf("failed output cannot also carry a successful result")
	}
	if err := out.Failure.Validate(); err != nil {
		return err
	}
	return nil
}

func (artifact ArtifactRef) Validate() error {
	if blank(artifact.ArtifactID, artifact.ContentHash, artifact.ObjectRef) || !strings.HasPrefix(artifact.ContentHash, "sha256:") {
		return fmt.Errorf("artifact requires identity, sha256 hash, and object reference")
	}
	if artifact.Kind != "" {
		switch artifact.Kind {
		case "page", "response", "failure", "listing_delta", "trace", "validation", "derived":
		default:
			return fmt.Errorf("artifact kind %q is not supported by the Recipe ABI", artifact.Kind)
		}
	}
	return nil
}

func (failure Failure) Validate() error {
	switch failure.Class {
	case "transport_timeout", "endpoint_rejected", "response_too_large", "redirect_rejected", "robots_disallowed", "throttled", "forbidden",
		"upstream_5xx", "unexpected_status", "auth_expired", "captcha", "parse_error", "quality_rejected", "contract_violated", "budget_revoked":
	default:
		return fmt.Errorf("unsupported failure class %q", failure.Class)
	}
	if failure.Signature != "" && !validFailureSignature(failure.Signature) {
		return fmt.Errorf("failure signature must be a normalized low-cardinality key")
	}
	if failure.Artifact.Kind != "" && failure.Artifact.Kind != "failure" {
		return fmt.Errorf("failure evidence Artifact must have failure kind")
	}
	return failure.Artifact.Validate()
}

func validFailureSignature(value string) bool {
	if value == "" || len(value) > 191 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func blank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}
