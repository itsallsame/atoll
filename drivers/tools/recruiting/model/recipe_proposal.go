package model

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// RecipeProposal records the provenance of one browser-extension capture.
// It is evidence for a draft Recipe, not authority to validate, publish, or
// assign that Recipe.
type RecipeProposal struct {
	CaptureID         string                   `json:"capture_id"`
	SourceID          string                   `json:"source_id"`
	SourceVersion     uint64                   `json:"source_version"`
	EndpointRevision  uint64                   `json:"endpoint_revision"`
	SourceURL         string                   `json:"source_url"`
	RecipeID          string                   `json:"recipe_id"`
	RecipeVersion     uint64                   `json:"recipe_version"`
	CaptureRef        string                   `json:"capture_ref"`
	CaptureHash       string                   `json:"capture_hash"`
	RecipeContentRef  string                   `json:"recipe_content_ref"`
	RecipeContentHash string                   `json:"recipe_content_hash"`
	CapturedBy        string                   `json:"captured_by"`
	CapturedAt        string                   `json:"captured_at"`
	Evidence          []RecipeProposalEvidence `json:"evidence"`
	Trace             []RecipeProposalTrace    `json:"trace"`
	StateVersion      uint64                   `json:"state_version"`
}

type RecipeProposalEvidence struct {
	ArtifactID  string `json:"artifact_id"`
	ContentHash string `json:"content_hash"`
	ObjectRef   string `json:"object_ref"`
	Kind        string `json:"kind,omitempty"`
}

type RecipeProposalTrace struct {
	Kind      string `json:"kind"`
	Selector  string `json:"selector,omitempty"`
	Field     string `json:"field,omitempty"`
	Attribute string `json:"attribute,omitempty"`
}

func (p RecipeProposal) Validate() error {
	identities := []string{p.CaptureID, p.SourceID, p.SourceURL, p.RecipeID, p.CaptureRef,
		p.CaptureHash, p.RecipeContentRef, p.RecipeContentHash, p.CapturedBy, p.CapturedAt}
	for _, value := range identities {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("Recipe proposal identities, references, hashes, actor, and time are required")
		}
	}
	if len(p.CaptureID) > 191 || len(p.SourceID) > 191 || len(p.RecipeID) > 191 || len(p.CapturedBy) > 191 ||
		len(p.CaptureRef) > 1024 || len(p.RecipeContentRef) > 1024 || p.SourceVersion == 0 ||
		p.EndpointRevision == 0 || p.RecipeVersion == 0 || p.StateVersion != 1 {
		return fmt.Errorf("Recipe proposal identities, references, and versions are invalid")
	}
	if !strings.HasPrefix(p.CaptureHash, "sha256:") || !strings.HasPrefix(p.RecipeContentHash, "sha256:") {
		return fmt.Errorf("Recipe proposal hashes must use sha256")
	}
	if _, err := time.Parse(time.RFC3339, p.CapturedAt); err != nil {
		return fmt.Errorf("Recipe proposal captured_at must be RFC3339")
	}
	target, err := url.Parse(p.SourceURL)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.Fragment != "" {
		return fmt.Errorf("Recipe proposal Source URL is invalid")
	}
	for index, ref := range []string{p.CaptureRef, p.RecipeContentRef} {
		parsed, parseErr := url.Parse(ref)
		if parseErr != nil || (parsed.Scheme != "artifact" && parsed.Scheme != "recipe") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("Recipe proposal Resource references must be opaque artifact:// or recipe:// references")
		}
		if index == 0 && parsed.Scheme != "artifact" {
			return fmt.Errorf("Recipe proposal Capture Resource must use artifact://")
		}
	}
	if len(p.Evidence) == 0 || len(p.Evidence) > 100 || len(p.Trace) == 0 || len(p.Trace) > 500 {
		return fmt.Errorf("Recipe proposal requires bounded evidence and trace")
	}
	seenEvidence := make(map[string]struct{}, len(p.Evidence))
	for _, evidence := range p.Evidence {
		if strings.TrimSpace(evidence.ArtifactID) == "" || !strings.HasPrefix(evidence.ContentHash, "sha256:") ||
			len(evidence.ArtifactID) > 191 {
			return fmt.Errorf("Recipe proposal evidence is invalid")
		}
		parsed, parseErr := url.Parse(evidence.ObjectRef)
		if parseErr != nil || parsed.Scheme != "artifact" || parsed.Host == "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("Recipe proposal evidence is invalid")
		}
		if _, exists := seenEvidence[evidence.ArtifactID]; exists {
			return fmt.Errorf("Recipe proposal evidence IDs must be unique")
		}
		seenEvidence[evidence.ArtifactID] = struct{}{}
	}
	for _, step := range p.Trace {
		switch step.Kind {
		case "navigate", "capture_artifact":
			if step.Selector != "" || step.Field != "" || step.Attribute != "" {
				return fmt.Errorf("Recipe proposal trace is invalid")
			}
		case "mark_collection", "mark_pagination":
			if strings.TrimSpace(step.Selector) == "" || step.Field != "" || step.Attribute != "" {
				return fmt.Errorf("Recipe proposal trace is invalid")
			}
		case "mark_field":
			if strings.TrimSpace(step.Selector) == "" || strings.TrimSpace(step.Field) == "" {
				return fmt.Errorf("Recipe proposal trace is invalid")
			}
		default:
			return fmt.Errorf("Recipe proposal trace is invalid")
		}
	}
	return nil
}
