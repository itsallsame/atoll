// Package extensioncapture validates evidence produced by an operator's
// browser extension. It only creates a Recipe proposal. Publication,
// assignment, Source changes, and Checkpoint commits remain Recruiting Actor
// commands and are intentionally absent from this package.
package extensioncapture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

const Version = "recruiting.extension-capture.v1"

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
	Version         string                  `json:"version"`
	CaptureID       string                  `json:"capture_id"`
	SourceID        string                  `json:"source_id"`
	EndpointVersion uint64                  `json:"endpoint_version"`
	SourceURL       string                  `json:"source_url"`
	CapturedBy      string                  `json:"captured_by"`
	CapturedAt      string                  `json:"captured_at"`
	UserConfirmed   bool                    `json:"user_confirmed"`
	Candidate       recipeabi.Spec          `json:"candidate"`
	BrowserPlan     *browserdriver.Plan     `json:"browser_plan,omitempty"`
	Artifacts       []recipeabi.ArtifactRef `json:"artifacts"`
	Trace           []TraceStep             `json:"trace"`
}

type Proposal struct {
	SourceID        string                  `json:"source_id"`
	EndpointVersion uint64                  `json:"endpoint_version"`
	SourceURL       string                  `json:"source_url"`
	Candidate       recipeabi.Spec          `json:"candidate"`
	BrowserPlan     *browserdriver.Plan     `json:"browser_plan,omitempty"`
	Evidence        []recipeabi.ArtifactRef `json:"evidence"`
	Trace           []TraceStep             `json:"trace"`
	CaptureID       string                  `json:"capture_id"`
	CapturedBy      string                  `json:"captured_by"`
	CapturedAt      string                  `json:"captured_at"`
	ContentHash     string                  `json:"content_hash"`
}

func (c Capture) Validate() error {
	if c.Version != Version || blank(c.CaptureID, c.SourceID, c.SourceURL, c.CapturedBy) || c.EndpointVersion == 0 || !c.UserConfirmed {
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
	if err := c.Candidate.Validate(); err != nil {
		return fmt.Errorf("extension candidate recipe: %w", err)
	}
	if c.Candidate.Transport == recipeabi.TransportBrowser {
		if c.BrowserPlan == nil {
			return fmt.Errorf("browser candidate requires a constrained browser plan")
		}
		if err := c.BrowserPlan.Validate(); err != nil {
			return fmt.Errorf("extension browser plan: %w", err)
		}
	} else if c.BrowserPlan != nil {
		return fmt.Errorf("non-browser candidate cannot carry browser actions")
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
			if err := validateSelector(step.Selector); err != nil || step.Field != "" {
				return fmt.Errorf("extension trace %d has invalid structural selector", index)
			}
		case TraceField:
			if err := validateSelector(step.Selector); err != nil || strings.TrimSpace(step.Field) == "" {
				return fmt.Errorf("extension trace %d has invalid field marker", index)
			}
		default:
			return fmt.Errorf("extension trace %d has unsupported kind %q", index, step.Kind)
		}
	}
	return nil
}

func (c Capture) Proposal() (Proposal, error) {
	if err := c.Validate(); err != nil {
		return Proposal{}, err
	}
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
	sum := sha256.Sum256(raw)
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
	}
	return Proposal{SourceID: c.SourceID, EndpointVersion: c.EndpointVersion, SourceURL: c.SourceURL,
		Candidate: candidate, BrowserPlan: browserPlan, Evidence: append([]recipeabi.ArtifactRef(nil), c.Artifacts...),
		Trace:     append([]TraceStep(nil), c.Trace...),
		CaptureID: c.CaptureID, CapturedBy: c.CapturedBy, CapturedAt: c.CapturedAt,
		ContentHash: "sha256:" + hex.EncodeToString(sum[:])}, nil
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
