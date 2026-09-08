// Package recipeabi defines the versioned, deterministic contract between the
// recruiting control plane, executor, and site recipes. It is application ABI,
// not an addition to Atoll's generic message protocol.
package recipeabi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const Version = "recruiting.recipe.v1"

type Kind string

const (
	KindListing   Kind = "listing"
	KindDetail    Kind = "detail"
	KindDiscovery Kind = "discovery"
)

type Transport string

const (
	TransportHTTPJSON  Transport = "http_json"
	TransportHTTPHTML  Transport = "http_html"
	TransportBrowser   Transport = "browser"
	TransportExtension Transport = "extension"
)

type RunInput struct {
	ABIVersion string         `json:"abi_version"`
	Target     TargetRef      `json:"target"`
	Endpoint   EndpointRef    `json:"endpoint"`
	Assignment AssignmentRef  `json:"assignment"`
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

type CheckpointRef struct {
	Version        uint64   `json:"version"`
	FrontierKeys   []string `json:"frontier_keys,omitempty"`
	LastActivityAt string   `json:"last_activity_at,omitempty"`
}

type BudgetRef struct {
	PermitID      string `json:"permit_id"`
	PolicyVersion uint64 `json:"policy_version"`
}

type AttemptFence struct {
	WorkID            string `json:"work_id"`
	AttemptID         string `json:"attempt_id"`
	AcceptanceVersion uint64 `json:"acceptance_version"`
	CompanyVersion    uint64 `json:"company_version"`
	SourceVersion     uint64 `json:"source_version"`
	ProfileVersion    uint64 `json:"profile_version,omitempty"`
}

func (in RunInput) Validate() error {
	if in.ABIVersion != Version {
		return fmt.Errorf("unsupported recipe ABI %q", in.ABIVersion)
	}
	if blank(in.Target.Kind, in.Target.ID) || in.Endpoint.Version == 0 || blank(in.Assignment.RecipeID, in.Assignment.ContractHash) ||
		in.Assignment.RecipeVersion == 0 || in.Assignment.AssignmentVersion == 0 || blank(in.Budget.PermitID) || in.Budget.PolicyVersion == 0 ||
		blank(in.Attempt.WorkID, in.Attempt.AttemptID) || in.Attempt.AcceptanceVersion == 0 || in.Attempt.CompanyVersion == 0 || in.Attempt.SourceVersion == 0 {
		return fmt.Errorf("complete target, endpoint, assignment, budget, and attempt fence are required")
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
	ABIVersion         string           `json:"abi_version"`
	Kind               Kind             `json:"kind"`
	RequiredCapability string           `json:"required_capability"`
	Transport          Transport        `json:"transport"`
	Request            ReadRequest      `json:"request"`
	Extraction         Extraction       `json:"extraction"`
	Listing            *ListingContract `json:"listing,omitempty"`
}

type ReadRequest struct {
	Method           string            `json:"method"`
	Headers          map[string]string `json:"headers,omitempty"`
	TimeoutMS        int               `json:"timeout_ms"`
	MaxResponseBytes int64             `json:"max_response_bytes"`
	MaxRedirects     int               `json:"max_redirects"`
	UserAgent        string            `json:"user_agent"`
}

type Extraction struct {
	Collection    string            `json:"collection,omitempty"`
	Fields        map[string]string `json:"fields"`
	Attributes    map[string]string `json:"attributes,omitempty"`
	Next          string            `json:"next,omitempty"`
	NextAttribute string            `json:"next_attribute,omitempty"`
}

type ListingContract struct {
	IdentityField      string `json:"identity_field"`
	ActivityField      string `json:"activity_field,omitempty"`
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
	case TransportHTTPJSON, TransportHTTPHTML, TransportBrowser, TransportExtension:
	default:
		return fmt.Errorf("unsupported recipe transport %q", s.Transport)
	}
	if strings.ToUpper(strings.TrimSpace(s.Request.Method)) != "GET" {
		return fmt.Errorf("recipe request must be read-only GET")
	}
	if s.Request.TimeoutMS < 100 || s.Request.TimeoutMS > 60_000 || s.Request.MaxResponseBytes < 1 || s.Request.MaxResponseBytes > 20<<20 ||
		s.Request.MaxRedirects < 0 || s.Request.MaxRedirects > 5 || strings.TrimSpace(s.Request.UserAgent) == "" {
		return fmt.Errorf("request budgets exceed the recipe ABI limits")
	}
	for name := range s.Request.Headers {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "accept", "accept-language":
		default:
			return fmt.Errorf("recipe header %q is not allowed", name)
		}
	}
	for name, value := range s.Request.Headers {
		if strings.ContainsAny(name+value, "\r\n") {
			return fmt.Errorf("recipe headers cannot contain control newlines")
		}
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
		for _, pointer := range []string{s.Extraction.Collection, s.Extraction.Next} {
			if pointer != "" && !strings.HasPrefix(pointer, "/") {
				return fmt.Errorf("JSON collection and next expressions must be JSON pointers")
			}
		}
	}
	if s.Transport == TransportHTTPHTML {
		for field, attribute := range s.Extraction.Attributes {
			if _, ok := s.Extraction.Fields[field]; !ok || !safeAttributeName(attribute) {
				return fmt.Errorf("HTML attributes must reference extracted fields and use safe names")
			}
		}
		if s.Extraction.NextAttribute != "" && !safeAttributeName(s.Extraction.NextAttribute) {
			return fmt.Errorf("HTML next_attribute is invalid")
		}
	}
	if s.Kind == KindListing {
		if err := s.Listing.Validate(); err != nil {
			return err
		}
		if _, ok := s.Extraction.Fields[s.Listing.IdentityField]; !ok {
			return fmt.Errorf("listing identity_field must name an extracted field")
		}
		if s.Transport == TransportHTTPJSON && s.Extraction.Collection == "" {
			return fmt.Errorf("JSON listing recipe requires a collection pointer")
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
	return nil
}

func (c *ListingContract) Validate() error {
	if c == nil || strings.TrimSpace(c.IdentityField) == "" || c.Ordering != "newest_activity_desc" || !c.UpdateRetop ||
		c.OverlapPages < 1 || c.OverlapPages > 20 || c.MaxPages < c.OverlapPages || c.MaxPages > 1000 ||
		c.MaxItemsPerPage < 1 || c.MaxItemsPerPage > 5000 || c.MaxTotalBytes < 1 || c.MaxTotalBytes > 1<<30 {
		return fmt.Errorf("listing recipe requires identity, descending activity/update-retop contract, and bounded overlap/pages")
	}
	if c.FrontierWidth < 1 || c.FrontierWidth > 100 {
		return fmt.Errorf("listing frontier width must be in [1,100]")
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

type ArtifactRef struct {
	ArtifactID  string `json:"artifact_id"`
	ContentHash string `json:"content_hash"`
	ObjectRef   string `json:"object_ref"`
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
	return nil
}

func (failure Failure) Validate() error {
	switch failure.Class {
	case "transport_timeout", "endpoint_rejected", "response_too_large", "redirect_rejected", "robots_disallowed", "throttled", "forbidden",
		"upstream_5xx", "unexpected_status", "auth_expired", "captcha", "parse_error", "quality_rejected", "contract_violated", "budget_revoked":
	default:
		return fmt.Errorf("unsupported failure class %q", failure.Class)
	}
	return failure.Artifact.Validate()
}

func blank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}
