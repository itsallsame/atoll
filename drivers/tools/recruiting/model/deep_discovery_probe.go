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
	ProbeID            string                       `json:"probe_id"`
	MissionID          string                       `json:"mission_id"`
	MissionVersion     uint64                       `json:"mission_version"`
	WorkID             string                       `json:"work_id"`
	URL                string                       `json:"url"`
	WaitSelector       string                       `json:"wait_selector,omitempty"`
	ScrollRepeats      int                          `json:"scroll_repeats,omitempty"`
	FollowLinkSelector string                       `json:"follow_link_selector,omitempty"`
	BrowserQueryMethod string                       `json:"browser_query_method,omitempty"`
	BrowserQueryPath   string                       `json:"browser_query_path,omitempty"`
	ListingAdvance     *DeepDiscoveryListingAdvance `json:"listing_advance,omitempty"`
	Status             DeepDiscoveryProbeStatus     `json:"status"`
	ArtifactID         string                       `json:"artifact_id,omitempty"`
	FinalURL           string                       `json:"final_url,omitempty"`
	ContentHash        string                       `json:"content_hash,omitempty"`
	LinkCount          int                          `json:"link_count,omitempty"`
	Version            uint64                       `json:"version"`
}

type DeepDiscoveryListingEndProof struct {
	Kind     string `json:"kind"`
	Pointer  string `json:"pointer,omitempty"`
	Selector string `json:"selector,omitempty"`
}

type DeepDiscoveryListingAdvance struct {
	Kind          string                       `json:"kind"`
	Selector      string                       `json:"selector,omitempty"`
	ProgressProof []string                     `json:"progress_proof"`
	EndProof      DeepDiscoveryListingEndProof `json:"end_proof"`
	WaitTimeoutMS int                          `json:"wait_timeout_ms"`
	MaxAdvances   int                          `json:"max_advances"`
	MaxNoProgress int                          `json:"max_no_progress"`
}

func NewDeepDiscoveryBrowserProbe(id, missionID, workID, rawURL, waitSelector string, scrollRepeats int,
	followLinkSelector string, missionVersion uint64) (DeepDiscoveryBrowserProbe, error) {
	id, missionID, workID = strings.TrimSpace(id), strings.TrimSpace(missionID), strings.TrimSpace(workID)
	waitSelector, followLinkSelector = strings.TrimSpace(waitSelector), strings.TrimSpace(followLinkSelector)
	canonical, err := CanonicalHTTPURL(rawURL)
	if err != nil || id == "" || missionID == "" || workID == "" || missionVersion == 0 ||
		len(id) > 191 || len(waitSelector) > 500 || len(followLinkSelector) > 500 || scrollRepeats < 0 || scrollRepeats > 10 {
		return DeepDiscoveryBrowserProbe{}, fmt.Errorf("deep discovery browser probe requires bounded identity, public URL, Mission version, and browser actions")
	}
	return DeepDiscoveryBrowserProbe{ProbeID: id, MissionID: missionID, MissionVersion: missionVersion,
		WorkID: workID, URL: canonical, WaitSelector: waitSelector, ScrollRepeats: scrollRepeats,
		FollowLinkSelector: followLinkSelector,
		Status:             DeepDiscoveryProbeQueued, Version: 1}, nil
}

func (p DeepDiscoveryBrowserProbe) WithListingAdvance(method, path string,
	advance DeepDiscoveryListingAdvance) (DeepDiscoveryBrowserProbe, error) {
	method, path = strings.ToUpper(strings.TrimSpace(method)), strings.TrimSpace(path)
	if p.Status != DeepDiscoveryProbeQueued || method != "POST" || !strings.HasPrefix(path, "/") ||
		strings.ContainsAny(path, "?#") || len(path) > 2048 {
		return DeepDiscoveryBrowserProbe{}, fmt.Errorf("listing advancement probe requires one bounded POST endpoint path")
	}
	p.BrowserQueryMethod, p.BrowserQueryPath, p.ListingAdvance = method, path, &advance
	return p, nil
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
