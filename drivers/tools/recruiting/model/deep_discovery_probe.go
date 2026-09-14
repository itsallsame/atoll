package model

import (
	"fmt"
	"strings"
)

type DeepDiscoveryProbeStatus string

const (
	DeepDiscoveryProbeQueued    DeepDiscoveryProbeStatus = "queued"
	DeepDiscoveryProbeCompleted DeepDiscoveryProbeStatus = "completed"
)

// DeepDiscoveryBrowserProbe is immutable browser intent plus its durable
// terminal evidence. Work/Attempt owns scheduling and execution lifecycle;
// the Mission owns research stage and budget.
type DeepDiscoveryBrowserProbe struct {
	ProbeID             string                   `json:"probe_id"`
	MissionID           string                   `json:"mission_id"`
	MissionVersion      uint64                   `json:"mission_version"`
	WorkID              string                   `json:"work_id"`
	URL                 string                   `json:"url"`
	WaitSelector        string                   `json:"wait_selector,omitempty"`
	ScrollRepeats       int                      `json:"scroll_repeats,omitempty"`
	FollowLinkSelector  string                   `json:"follow_link_selector,omitempty"`
	StubVerificationIDs []string                 `json:"stub_verification_ids,omitempty"`
	Status              DeepDiscoveryProbeStatus `json:"status"`
	ArtifactID          string                   `json:"artifact_id,omitempty"`
	FinalURL            string                   `json:"final_url,omitempty"`
	ContentHash         string                   `json:"content_hash,omitempty"`
	LinkCount           int                      `json:"link_count,omitempty"`
	Version             uint64                   `json:"version"`
}

func NewDeepDiscoveryBrowserProbe(id, missionID, workID, rawURL, waitSelector string, scrollRepeats int,
	followLinkSelector string, missionVersion uint64, stubVerificationIDs ...string) (DeepDiscoveryBrowserProbe, error) {
	id, missionID, workID = strings.TrimSpace(id), strings.TrimSpace(missionID), strings.TrimSpace(workID)
	waitSelector, followLinkSelector = strings.TrimSpace(waitSelector), strings.TrimSpace(followLinkSelector)
	canonical, err := CanonicalHTTPURL(rawURL)
	if err != nil || id == "" || missionID == "" || workID == "" || missionVersion == 0 ||
		len(id) > 191 || len(waitSelector) > 500 || len(followLinkSelector) > 500 || scrollRepeats < 0 || scrollRepeats > 10 ||
		len(stubVerificationIDs) > 10 {
		return DeepDiscoveryBrowserProbe{}, fmt.Errorf("deep discovery browser probe requires bounded identity, public URL, Mission version, and browser actions")
	}
	seenStubs := make(map[string]struct{}, len(stubVerificationIDs))
	normalizedStubs := make([]string, len(stubVerificationIDs))
	for index, verificationID := range stubVerificationIDs {
		normalizedStubs[index] = strings.TrimSpace(verificationID)
		if normalizedStubs[index] == "" || len(normalizedStubs[index]) > 191 {
			return DeepDiscoveryBrowserProbe{}, fmt.Errorf("deep discovery browser probe contains an invalid Stub verification identity")
		}
		if _, duplicate := seenStubs[normalizedStubs[index]]; duplicate {
			return DeepDiscoveryBrowserProbe{}, fmt.Errorf("deep discovery browser probe contains duplicate Stub verification identity")
		}
		seenStubs[normalizedStubs[index]] = struct{}{}
	}
	return DeepDiscoveryBrowserProbe{ProbeID: id, MissionID: missionID, MissionVersion: missionVersion,
		WorkID: workID, URL: canonical, WaitSelector: waitSelector, ScrollRepeats: scrollRepeats,
		FollowLinkSelector: followLinkSelector, StubVerificationIDs: normalizedStubs,
		Status: DeepDiscoveryProbeQueued, Version: 1}, nil
}

func (p DeepDiscoveryBrowserProbe) Complete(expected uint64, artifactID, finalURL, contentHash string, linkCount int) (DeepDiscoveryBrowserProbe, error) {
	artifactID, contentHash = strings.TrimSpace(artifactID), strings.TrimSpace(contentHash)
	canonical, err := CanonicalHTTPURL(finalURL)
	if err != nil || expected != p.Version || p.Status != DeepDiscoveryProbeQueued || artifactID == "" ||
		contentHash == "" || linkCount < 0 || linkCount > 200 {
		return DeepDiscoveryBrowserProbe{}, fmt.Errorf("queued deep discovery browser probe and bounded terminal evidence are required")
	}
	p.Status, p.ArtifactID, p.FinalURL, p.ContentHash, p.LinkCount = DeepDiscoveryProbeCompleted, artifactID, canonical, contentHash, linkCount
	p.Version++
	return p, nil
}
