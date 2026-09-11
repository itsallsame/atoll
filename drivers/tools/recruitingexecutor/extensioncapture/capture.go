// Package extensioncapture validates evidence produced by an operator's
// browser extension. It only creates a Recipe proposal. Publication,
// assignment, Source changes, and Checkpoint commits remain Recruiting Actor
// commands and are intentionally absent from this package.
package extensioncapture

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
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

const (
	LegacyVersion = "recruiting.extension-capture.v1"
	Version       = "recruiting.extension-capture.v2"
)

// MaxCaptureBytes bounds the Resource before JSON decoding. Captures contain
// only a structural trace and evidence references, never page bodies.
const MaxCaptureBytes = 1 << 20

type TraceKind string

const (
	TraceNavigate   TraceKind = "navigate"
	TraceCollection TraceKind = "mark_collection"
	TraceField      TraceKind = "mark_field"
	TracePagination TraceKind = "mark_pagination"
	TraceArtifact   TraceKind = "capture_artifact"
)

// TraceStep stores structural observations only. There is deliberately no
// typed value, request body, cookie, storage, or credential field.
type TraceStep struct {
	Kind      TraceKind `json:"kind"`
	Selector  string    `json:"selector,omitempty"`
	Field     string    `json:"field,omitempty"`
	Attribute string    `json:"attribute,omitempty"`
}

type Capture struct {
	Version          string                  `json:"version"`
	CaptureID        string                  `json:"capture_id"`
	SourceID         string                  `json:"source_id"`
	EndpointVersion  uint64                  `json:"endpoint_version"`
	SourceURL        string                  `json:"source_url"`
	PageURL          string                  `json:"page_url,omitempty"`
	SampleJobID      string                  `json:"sample_job_id,omitempty"`
	SampleJobVersion uint64                  `json:"sample_job_version,omitempty"`
	CapturedBy       string                  `json:"captured_by"`
	CapturedAt       string                  `json:"captured_at"`
	UserConfirmed    bool                    `json:"user_confirmed"`
	Candidate        recipeabi.Spec          `json:"candidate"`
	BrowserPlan      *browserdriver.Plan     `json:"browser_plan,omitempty"`
	Artifacts        []recipeabi.ArtifactRef `json:"artifacts"`
	Trace            []TraceStep             `json:"trace"`
}

type Proposal struct {
	SourceID         string                  `json:"source_id"`
	EndpointVersion  uint64                  `json:"endpoint_version"`
	SourceURL        string                  `json:"source_url"`
	PageURL          string                  `json:"page_url"`
	SampleJobID      string                  `json:"sample_job_id,omitempty"`
	SampleJobVersion uint64                  `json:"sample_job_version,omitempty"`
	Candidate        recipeabi.Spec          `json:"candidate"`
	BrowserPlan      *browserdriver.Plan     `json:"browser_plan,omitempty"`
	Evidence         []recipeabi.ArtifactRef `json:"evidence"`
	Trace            []TraceStep             `json:"trace"`
	CaptureID        string                  `json:"capture_id"`
	CapturedBy       string                  `json:"captured_by"`
	CapturedAt       string                  `json:"captured_at"`
	ContentHash      string                  `json:"content_hash"`
}

// DecodeCapture is the single strict decoder used at the browser-extension
// trust boundary. Unknown fields and concatenated JSON values are rejected so
// the extension and control plane cannot silently disagree about semantics.
func DecodeCapture(raw []byte) (Capture, error) {
	if len(raw) == 0 || len(raw) > MaxCaptureBytes {
		return Capture{}, fmt.Errorf("extension capture resource size must be in [1,%d] bytes", MaxCaptureBytes)
	}
	var capture Capture
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capture); err != nil {
		return Capture{}, fmt.Errorf("decode extension capture resource: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Capture{}, errors.New("decode extension capture resource: multiple JSON values")
		}
		return Capture{}, fmt.Errorf("decode extension capture resource: %w", err)
	}
	if err := capture.Validate(); err != nil {
		return Capture{}, fmt.Errorf("validate extension capture resource: %w", err)
	}
	return capture, nil
}

func (c Capture) Validate() error {
	if (c.Version != Version && c.Version != LegacyVersion) || blank(c.CaptureID, c.SourceID, c.SourceURL, c.CapturedBy) || c.EndpointVersion == 0 || !c.UserConfirmed {
		return fmt.Errorf("extension capture requires version, identities, endpoint version, and user confirmation")
	}
	if _, err := time.Parse(time.RFC3339, c.CapturedAt); err != nil {
		return fmt.Errorf("extension capture time must be RFC3339")
	}
	target, err := url.Parse(c.SourceURL)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.Fragment != "" {
		return fmt.Errorf("extension source must be an absolute HTTPS URL without credentials or fragment")
	}
	for key := range target.Query() {
		lower := strings.ToLower(key)
		for _, forbidden := range []string{"token", "secret", "password", "session", "signature", "authorization", "api_key", "apikey"} {
			if strings.Contains(lower, forbidden) {
				return fmt.Errorf("extension source query appears to contain secret material")
			}
		}
	}
	pageURL := c.PageURL
	if pageURL == "" {
		pageURL = c.SourceURL
	}
	page, pageErr := url.Parse(pageURL)
	if pageErr != nil || page.Scheme != "https" || page.Host == "" || page.User != nil || page.Fragment != "" {
		return fmt.Errorf("extension captured page must be an absolute HTTPS URL without credentials or fragment")
	}
	for key := range page.Query() {
		lower := strings.ToLower(key)
		for _, forbidden := range []string{"token", "secret", "password", "session", "signature", "authorization", "api_key", "apikey"} {
			if strings.Contains(lower, forbidden) {
				return fmt.Errorf("extension captured page query appears to contain secret material")
			}
		}
	}
	candidate := c.Candidate
	if candidate.Transport == recipeabi.TransportBrowser {
		if c.BrowserPlan == nil {
			return fmt.Errorf("browser candidate requires a constrained browser plan")
		}
		if candidate.BrowserPlan != nil {
			candidateHash, _ := candidate.BrowserPlan.ContentHash()
			captureHash, _ := c.BrowserPlan.ContentHash()
			if candidateHash == "" || candidateHash != captureHash {
				return fmt.Errorf("browser candidate and capture plans differ")
			}
		}
		candidate.BrowserPlan = c.BrowserPlan
	} else if c.BrowserPlan != nil || candidate.BrowserPlan != nil {
		return fmt.Errorf("non-browser candidate cannot carry browser actions")
	}
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("extension candidate recipe: %w", err)
	}
	if c.Version == LegacyVersion && (c.Candidate.Kind != recipeabi.KindListing || c.PageURL != "" ||
		c.SampleJobID != "" || c.SampleJobVersion != 0) {
		return fmt.Errorf("legacy extension capture supports only its original Listing shape")
	}
	switch c.Candidate.Kind {
	case recipeabi.KindListing:
		if pageURL != c.SourceURL || c.SampleJobID != "" || c.SampleJobVersion != 0 {
			return fmt.Errorf("listing capture must target the active Source page")
		}
	case recipeabi.KindDetail:
		if strings.TrimSpace(c.SampleJobID) == "" || len(c.SampleJobID) > 191 || c.SampleJobVersion == 0 {
			return fmt.Errorf("detail capture requires a versioned Source Job sample")
		}
	case recipeabi.KindDiscovery:
		return fmt.Errorf("Source-bound extension capture does not support discovery Recipes")
	}
	if len(c.Artifacts) == 0 || len(c.Artifacts) > 100 || len(c.Trace) == 0 || len(c.Trace) > 500 {
		return fmt.Errorf("extension capture requires bounded artifact evidence and trace")
	}
	seenArtifacts := make(map[string]struct{}, len(c.Artifacts))
	for _, artifact := range c.Artifacts {
		if blank(artifact.ArtifactID, artifact.ContentHash, artifact.ObjectRef) || !strings.HasPrefix(artifact.ContentHash, "sha256:") {
			return fmt.Errorf("extension artifact reference is incomplete")
		}
		if _, exists := seenArtifacts[artifact.ArtifactID]; exists {
			return fmt.Errorf("extension artifact IDs must be unique")
		}
		objectRef, err := url.Parse(artifact.ObjectRef)
		if err != nil || objectRef.Scheme != "artifact" || objectRef.Host == "" || objectRef.User != nil || objectRef.RawQuery != "" || objectRef.Fragment != "" {
			return fmt.Errorf("extension artifact object reference must be opaque artifact:// without credentials or query")
		}
		seenArtifacts[artifact.ArtifactID] = struct{}{}
	}
	for index, step := range c.Trace {
		switch step.Kind {
		case TraceNavigate, TraceArtifact:
			if step.Selector != "" || step.Field != "" || step.Attribute != "" {
				return fmt.Errorf("extension trace %d stores unexpected values", index)
			}
		case TraceCollection, TracePagination:
			if step.Field != "" || step.Attribute != "" || !c.traceSelectorValid(step) {
				return fmt.Errorf("extension trace %d has invalid structural selector", index)
			}
		case TraceField:
			if strings.TrimSpace(step.Field) == "" || c.Candidate.Extraction.Fields[step.Field] != step.Selector ||
				c.Candidate.Extraction.Attributes[step.Field] != step.Attribute {
				return fmt.Errorf("extension trace %d has invalid field marker", index)
			}
		default:
			return fmt.Errorf("extension trace %d has unsupported kind %q", index, step.Kind)
		}
	}
	return nil
}

func (c Capture) traceSelectorValid(step TraceStep) bool {
	if step.Kind == TraceCollection {
		return step.Selector == c.Candidate.Extraction.Collection
	}
	if c.Candidate.Transport == recipeabi.TransportHTTPJSON {
		return strings.HasPrefix(step.Selector, "/")
	}
	return validateSelector(step.Selector) == nil
}

func (c Capture) Proposal() (Proposal, error) {
	if err := c.Validate(); err != nil {
		return Proposal{}, err
	}
	if c.Version == LegacyVersion {
		unsigned := struct {
			SourceID        string
			EndpointVersion uint64
			SourceURL       string
			Candidate       recipeabi.Spec
			BrowserPlan     *browserdriver.Plan
			Evidence        []recipeabi.ArtifactRef
			Trace           []TraceStep
			CaptureID       string
			CapturedBy      string
			CapturedAt      string
		}{
			SourceID: c.SourceID, EndpointVersion: c.EndpointVersion, SourceURL: c.SourceURL,
			Candidate: c.Candidate, BrowserPlan: c.BrowserPlan, Evidence: c.Artifacts, Trace: c.Trace,
			CaptureID: c.CaptureID, CapturedBy: c.CapturedBy, CapturedAt: c.CapturedAt,
		}
		raw, err := json.Marshal(unsigned)
		if err != nil {
			return Proposal{}, err
		}
		return c.proposalWithHash(raw)
	}
	unsigned := struct {
		SourceID         string
		EndpointVersion  uint64
		SourceURL        string
		PageURL          string
		SampleJobID      string
		SampleJobVersion uint64
		Candidate        recipeabi.Spec
		BrowserPlan      *browserdriver.Plan
		Evidence         []recipeabi.ArtifactRef
		Trace            []TraceStep
		CaptureID        string
		CapturedBy       string
		CapturedAt       string
	}{
		SourceID: c.SourceID, EndpointVersion: c.EndpointVersion, SourceURL: c.SourceURL,
		PageURL: pageURL(c), SampleJobID: c.SampleJobID, SampleJobVersion: c.SampleJobVersion,
		Candidate: c.Candidate, BrowserPlan: c.BrowserPlan, Evidence: c.Artifacts, Trace: c.Trace,
		CaptureID: c.CaptureID, CapturedBy: c.CapturedBy, CapturedAt: c.CapturedAt,
	}
	raw, err := json.Marshal(unsigned)
	if err != nil {
		return Proposal{}, err
	}
	return c.proposalWithHash(raw)
}

func (c Capture) proposalWithHash(unsigned []byte) (Proposal, error) {
	sum := sha256.Sum256(unsigned)
	candidate, err := cloneJSON(c.Candidate)
	if err != nil {
		return Proposal{}, err
	}
	var browserPlan *browserdriver.Plan
	if c.BrowserPlan != nil {
		plan, cloneErr := cloneJSON(*c.BrowserPlan)
		if cloneErr != nil {
			return Proposal{}, cloneErr
		}
		browserPlan = &plan
		candidate.BrowserPlan = browserPlan
	}
	return Proposal{SourceID: c.SourceID, EndpointVersion: c.EndpointVersion, SourceURL: c.SourceURL,
		PageURL: pageURL(c), SampleJobID: c.SampleJobID, SampleJobVersion: c.SampleJobVersion,
		Candidate: candidate, BrowserPlan: browserPlan, Evidence: append([]recipeabi.ArtifactRef(nil), c.Artifacts...),
		Trace:     append([]TraceStep(nil), c.Trace...),
		CaptureID: c.CaptureID, CapturedBy: c.CapturedBy, CapturedAt: c.CapturedAt,
		ContentHash: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

func pageURL(c Capture) string {
	if c.PageURL != "" {
		return c.PageURL
	}
	return c.SourceURL
}

func cloneJSON[T any](value T) (T, error) {
	var clone T
	raw, err := json.Marshal(value)
	if err != nil {
		return clone, err
	}
	if err := json.Unmarshal(raw, &clone); err != nil {
		return clone, err
	}
	return clone, nil
}

func validateSelector(selector string) error {
	if strings.TrimSpace(selector) == "" {
		return fmt.Errorf("selector is empty")
	}
	_, err := cascadia.Parse(selector)
	return err
}

func blank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}
