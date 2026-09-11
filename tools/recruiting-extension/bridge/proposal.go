// Package bridge connects the optional recruiting browser extension to the
// existing Atoll public Resource and Message protocols. It is a client-side
// adapter, not an Actor, Worker, scheduler, or alternate control plane.
package bridge

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

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/extensioncapture"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

const (
	DraftVersion     = "recruiting.extension-draft.v1"
	MaxEvidenceBytes = 512 << 10
)

// Draft is the untrusted browser-side result. Source versions, Endpoint URL,
// CapturedBy, timestamps, Resource references, and hashes are deliberately not
// accepted from the extension; the authenticated local bridge supplies them.
type Draft struct {
	Version       string                       `json:"version"`
	CaptureID     string                       `json:"capture_id"`
	SourceID      string                       `json:"source_id"`
	RecipeID      string                       `json:"recipe_id"`
	RecipeVersion uint64                       `json:"recipe_version"`
	PageURL       string                       `json:"page_url"`
	SampleJobID   string                       `json:"sample_job_id,omitempty"`
	CapturedAt    string                       `json:"captured_at"`
	UserConfirmed bool                         `json:"user_confirmed"`
	Candidate     recipeabi.Spec               `json:"candidate"`
	BrowserPlan   *browserdriver.Plan          `json:"browser_plan,omitempty"`
	Trace         []extensioncapture.TraceStep `json:"trace"`
	Evidence      json.RawMessage              `json:"evidence"`
}

type SourceFence struct {
	SourceID         string
	SourceVersion    uint64
	EndpointRevision uint64
	EndpointURL      string
	ReadinessStatus  model.SourceReadinessStatus
	ControlStatus    model.ControlStatus
	HealthStatus     model.HealthStatus
	SampleJobID      string
	SampleJobVersion uint64
	SampleJobURL     string
}

type ResourceSet struct {
	EvidenceRef  string
	EvidenceBody json.RawMessage
	RecipeRef    string
	RecipeBody   json.RawMessage
	CaptureRef   string
	CaptureBody  json.RawMessage
	Proposal     map[string]any
	Capture      extensioncapture.Capture
}

func Build(draft Draft, source SourceFence, capturedBy string, receivedAt time.Time) (ResourceSet, error) {
	if draft.Version != DraftVersion || strings.TrimSpace(draft.CaptureID) == "" ||
		strings.TrimSpace(draft.SourceID) == "" || strings.TrimSpace(draft.RecipeID) == "" ||
		draft.RecipeVersion == 0 || !draft.UserConfirmed {
		return ResourceSet{}, fmt.Errorf("extension draft requires version, identities, Recipe version, and user confirmation")
	}
	if len(draft.CaptureID) > 160 || !safeSegment(draft.CaptureID) || len(draft.SourceID) > 191 ||
		len(draft.RecipeID) > 160 || strings.TrimSpace(capturedBy) == "" || len(capturedBy) > 191 || receivedAt.IsZero() {
		return ResourceSet{}, fmt.Errorf("extension draft identities and authenticated capture facts are invalid")
	}
	draftCapturedAt, err := time.Parse(time.RFC3339, draft.CapturedAt)
	if err != nil || draftCapturedAt.After(receivedAt.Add(5*time.Minute)) {
		return ResourceSet{}, fmt.Errorf("extension draft captured_at is invalid or ahead of the Bridge clock")
	}
	if source.SourceID != draft.SourceID || source.SourceVersion == 0 || source.EndpointRevision == 0 ||
		source.ReadinessStatus != model.SourceReady || source.ControlStatus != model.ControlActive ||
		source.HealthStatus != model.HealthHealthy {
		return ResourceSet{}, fmt.Errorf("extension proposal requires the current ready, active, healthy Source fence")
	}
	pageURL, pageErr := url.Parse(draft.PageURL)
	endpointURL, endpointErr := url.Parse(source.EndpointURL)
	if pageErr != nil || endpointErr != nil || pageURL.Scheme != "https" || endpointURL.Scheme != "https" ||
		pageURL.User != nil || pageURL.Fragment != "" {
		return ResourceSet{}, fmt.Errorf("captured page and active Source Endpoint must be safe HTTPS URLs")
	}
	candidate := draft.Candidate
	if candidate.Transport == recipeabi.TransportBrowser {
		if draft.BrowserPlan == nil {
			return ResourceSet{}, fmt.Errorf("browser draft requires a constrained browser plan")
		}
		if candidate.BrowserPlan != nil {
			candidateHash, _ := candidate.BrowserPlan.ContentHash()
			draftHash, _ := draft.BrowserPlan.ContentHash()
			if candidateHash == "" || candidateHash != draftHash {
				return ResourceSet{}, fmt.Errorf("browser draft candidate and plan differ")
			}
		}
		candidate.BrowserPlan = draft.BrowserPlan
	} else if draft.BrowserPlan != nil || candidate.BrowserPlan != nil {
		return ResourceSet{}, fmt.Errorf("non-browser draft cannot carry a browser plan")
	}
	if err := candidate.Validate(); err != nil {
		return ResourceSet{}, fmt.Errorf("extension draft candidate: %w", err)
	}
	switch candidate.Kind {
	case recipeabi.KindListing:
		if draft.PageURL != source.EndpointURL || draft.SampleJobID != "" {
			return ResourceSet{}, fmt.Errorf("Listing capture must exactly match the active Source Endpoint")
		}
	case recipeabi.KindDetail:
		if draft.SampleJobID == "" || draft.SampleJobID != source.SampleJobID || source.SampleJobVersion == 0 ||
			draft.PageURL != source.SampleJobURL {
			return ResourceSet{}, fmt.Errorf("Detail capture must match the authoritative Source Job sample")
		}
	default:
		return ResourceSet{}, fmt.Errorf("Source-bound extension capture supports Listing and Detail Recipes")
	}
	if len(draft.Evidence) == 0 || len(draft.Evidence) > MaxEvidenceBytes || !singleJSONValue(draft.Evidence) {
		return ResourceSet{}, fmt.Errorf("extension evidence must be one bounded JSON value")
	}
	base := "recruiting-capture/" + draft.CaptureID
	evidenceRef := "artifact://" + base + "/page"
	recipeRef := "recipe://" + base + "/candidate"
	captureRef := "artifact://" + base + "/capture"
	evidenceHash := contentHash(draft.Evidence)
	recipeBody, err := json.Marshal(candidate)
	if err != nil {
		return ResourceSet{}, fmt.Errorf("encode candidate Recipe: %w", err)
	}
	candidate, err = recipeabi.DecodeSpec(recipeBody)
	if err != nil {
		return ResourceSet{}, err
	}
	recipeHash, err := candidate.ContentHash()
	if err != nil {
		return ResourceSet{}, fmt.Errorf("hash candidate Recipe: %w", err)
	}
	capture := extensioncapture.Capture{Version: extensioncapture.Version, CaptureID: draft.CaptureID,
		SourceID: draft.SourceID, EndpointVersion: source.EndpointRevision, SourceURL: source.EndpointURL,
		PageURL: draft.PageURL, SampleJobID: source.SampleJobID, SampleJobVersion: source.SampleJobVersion,
		CapturedBy: capturedBy, CapturedAt: draftCapturedAt.UTC().Format(time.RFC3339), UserConfirmed: true,
		Candidate: candidate, BrowserPlan: draft.BrowserPlan,
		Artifacts: []recipeabi.ArtifactRef{{ArtifactID: "capture-page-" + draft.CaptureID,
			ContentHash: evidenceHash, ObjectRef: evidenceRef, Kind: "extension_capture_page"}},
		Trace: append([]extensioncapture.TraceStep(nil), draft.Trace...)}
	proposal, err := capture.Proposal()
	if err != nil {
		return ResourceSet{}, err
	}
	captureBody, err := json.Marshal(capture)
	if err != nil {
		return ResourceSet{}, fmt.Errorf("encode Extension Capture: %w", err)
	}
	return ResourceSet{EvidenceRef: evidenceRef, EvidenceBody: append(json.RawMessage(nil), draft.Evidence...),
		RecipeRef: recipeRef, RecipeBody: recipeBody, CaptureRef: captureRef, CaptureBody: captureBody, Capture: capture,
		Proposal: map[string]any{
			"command_id":       "extension-propose-" + draft.CaptureID,
			"target":           map[string]any{"target_type": "source", "target_id": draft.SourceID},
			"expected_version": source.SourceVersion, "recipe_id": draft.RecipeID,
			"recipe_version": draft.RecipeVersion, "endpoint_revision": source.EndpointRevision,
			"content_ref": recipeRef, "expected_content_hash": recipeHash,
			"capture_ref": captureRef, "expected_capture_hash": proposal.ContentHash,
			"reason": "operator confirmed browser Extension Capture",
		}}, nil
}

func DecodeDraft(raw []byte) (Draft, error) {
	if len(raw) == 0 || len(raw) > extensioncapture.MaxCaptureBytes {
		return Draft{}, fmt.Errorf("extension draft size must be in [1,%d] bytes", extensioncapture.MaxCaptureBytes)
	}
	var draft Draft
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return Draft{}, fmt.Errorf("decode extension draft: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Draft{}, fmt.Errorf("decode extension draft: multiple JSON values")
		}
		return Draft{}, fmt.Errorf("decode extension draft: %w", err)
	}
	return draft, nil
}

func singleJSONValue(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	return errors.Is(decoder.Decode(&value), io.EOF)
}

func contentHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func safeSegment(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}
