package recipeexec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type ListingScan struct {
	spec             recipeabi.Spec
	checkpoint       *recipeabi.CheckpointRef
	baseline         bool
	boundaryTime     time.Time
	frontierNeeded   map[string]struct{}
	frontierSeen     map[string]struct{}
	seenItems        map[string][]byte
	items            []map[string]json.RawMessage
	topFrontier      []string
	topActivity      time.Time
	previousActivity time.Time
	pages            int
	boundaryPage     int
	complete         bool
	stopReason       string
	quality          recipeabi.QualityProof
}

func NewListingScan(spec recipeabi.Spec, checkpoint *recipeabi.CheckpointRef) (*ListingScan, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Kind != recipeabi.KindListing {
		return nil, fmt.Errorf("listing scan requires a listing recipe")
	}
	scan := &ListingScan{
		spec: spec, checkpoint: checkpoint, baseline: checkpoint == nil,
		frontierNeeded: map[string]struct{}{}, frontierSeen: map[string]struct{}{}, seenItems: map[string][]byte{},
		quality: recipeabi.QualityProof{IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true},
	}
	if checkpoint == nil {
		return scan, nil
	}
	if checkpoint.Version == 0 {
		return nil, fmt.Errorf("incremental listing scan requires a versioned checkpoint")
	}
	switch spec.Listing.BoundaryMode {
	case "activity_time":
		if checkpoint.LastActivityAt == "" {
			return nil, fmt.Errorf("activity_time scan requires checkpoint last_activity_at")
		}
		boundary, err := time.Parse(time.RFC3339, checkpoint.LastActivityAt)
		if err != nil {
			return nil, fmt.Errorf("parse checkpoint activity boundary: %w", err)
		}
		scan.boundaryTime = boundary
	case "frontier_keys":
		if len(checkpoint.FrontierKeys) == 0 {
			return nil, fmt.Errorf("frontier_keys scan requires checkpoint frontier keys")
		}
		for _, key := range checkpoint.FrontierKeys {
			key = strings.TrimSpace(key)
			if key == "" {
				return nil, fmt.Errorf("checkpoint frontier keys must be non-empty")
			}
			scan.frontierNeeded[key] = struct{}{}
		}
	}
	return scan, nil
}

func (s *ListingScan) AddPage(page DocumentResult) error {
	if s.complete {
		return fmt.Errorf("listing scan is already complete")
	}
	if s.pages >= s.spec.Listing.MaxPages {
		return fmt.Errorf("listing scan already reached max pages")
	}
	s.pages++
	s.quality.IdentityComplete = s.quality.IdentityComplete && page.Quality.IdentityComplete
	s.quality.OrderingContractHeld = s.quality.OrderingContractHeld && page.Quality.OrderingContractHeld
	s.quality.PaginationStable = s.quality.PaginationStable && page.Quality.PaginationStable

	for index, item := range page.Items {
		identity, ok := rawIdentity(item[s.spec.Listing.IdentityField])
		identity = strings.TrimSpace(identity)
		if !ok || identity == "" {
			s.quality.IdentityComplete = false
			continue
		}
		canonical, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("canonicalize page %d item %d: %w", s.pages, index, err)
		}
		if previous, duplicate := s.seenItems[identity]; duplicate {
			if !bytes.Equal(previous, canonical) {
				s.quality.PaginationStable = false
			}
		} else {
			s.seenItems[identity] = canonical
			s.items = append(s.items, cloneItem(item))
			if len(s.topFrontier) < s.spec.Listing.FrontierWidth {
				s.topFrontier = append(s.topFrontier, identity)
			}
		}

		if _, needed := s.frontierNeeded[identity]; needed {
			s.frontierSeen[identity] = struct{}{}
			if s.boundaryPage == 0 && len(s.frontierSeen) == len(s.frontierNeeded) {
				s.markBoundaryReached()
			}
		}
		if s.spec.Listing.ActivityField != "" {
			activityText, activityOK := rawString(item[s.spec.Listing.ActivityField])
			activity, parseErr := time.Parse(time.RFC3339, activityText)
			if !activityOK || parseErr != nil {
				s.quality.OrderingContractHeld = false
				continue
			}
			if s.topActivity.IsZero() {
				s.topActivity = activity
			}
			if !s.previousActivity.IsZero() && activity.After(s.previousActivity) {
				s.quality.OrderingContractHeld = false
			}
			s.previousActivity = activity
			if s.spec.Listing.BoundaryMode == "activity_time" && s.boundaryPage == 0 && activity.Before(s.boundaryTime) {
				// Strictly older means every item in the checkpoint's equal-time
				// group has already been consumed, including groups split by pages.
				s.markBoundaryReached()
			}
		}
	}

	if s.boundaryPage != 0 && s.pages-s.boundaryPage >= s.spec.Listing.OverlapPages {
		s.quality.OverlapCompleted = true
		s.complete, s.stopReason = true, "safe_boundary"
	}
	if pageEndsInput(page.Next) {
		if s.baseline {
			s.quality.PreviousFrontierReached = true
		}
		if s.quality.PreviousFrontierReached {
			s.quality.OverlapCompleted = true
		}
		s.complete, s.stopReason = true, "end_of_input"
	}
	if !s.complete && s.pages >= s.spec.Listing.MaxPages {
		s.complete, s.stopReason = true, "max_pages"
	}
	s.quality.ItemCount = len(s.items)
	return nil
}

func (s *ListingScan) markBoundaryReached() {
	s.boundaryPage = s.pages
	s.quality.PreviousFrontierReached = true
}

func (s *ListingScan) Complete() bool { return s.complete }

func (s *ListingScan) StopReason() string { return s.stopReason }

func (s *ListingScan) Quality() recipeabi.QualityProof { return s.quality }

func (s *ListingScan) ItemCount() int { return len(s.items) }

// ItemsFrom returns newly accepted unique items without copying the full scan
// prefix on every page. The caller records these against that page's Artifact.
func (s *ListingScan) ItemsFrom(index int) ([]map[string]json.RawMessage, error) {
	if index < 0 || index > len(s.items) {
		return nil, fmt.Errorf("listing item offset %d is outside [0,%d]", index, len(s.items))
	}
	out := make([]map[string]json.RawMessage, 0, len(s.items)-index)
	for _, item := range s.items[index:] {
		out = append(out, cloneItem(item))
	}
	return out, nil
}

func (s *ListingScan) Items() []map[string]json.RawMessage {
	out, _ := s.ItemsFrom(0)
	return out
}

func (s *ListingScan) CheckpointCandidate() (recipeabi.CheckpointRef, error) {
	if !s.complete || !s.quality.MayAdvanceCheckpoint() || len(s.topFrontier) == 0 {
		return recipeabi.CheckpointRef{}, fmt.Errorf("listing scan lacks complete checkpoint proof")
	}
	version := uint64(1)
	if s.checkpoint != nil {
		version = s.checkpoint.Version + 1
	}
	candidate := recipeabi.CheckpointRef{Version: version, FrontierKeys: append([]string(nil), s.topFrontier...)}
	if !s.topActivity.IsZero() {
		candidate.LastActivityAt = s.topActivity.UTC().Format(time.RFC3339)
	}
	return candidate, nil
}

func pageEndsInput(next json.RawMessage) bool {
	if len(next) == 0 || bytes.Equal(bytes.TrimSpace(next), []byte("null")) {
		return true
	}
	var text string
	return json.Unmarshal(next, &text) == nil && strings.TrimSpace(text) == ""
}

func cloneItem(item map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(item))
	for key, value := range item {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}
