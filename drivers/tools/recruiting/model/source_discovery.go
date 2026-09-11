package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type SourceDiscoveryStatus string

const (
	SourceDiscoveryQueued    SourceDiscoveryStatus = "queued"
	SourceDiscoveryRunning   SourceDiscoveryStatus = "running"
	SourceDiscoveryCompleted SourceDiscoveryStatus = "completed"
	SourceDiscoveryFailed    SourceDiscoveryStatus = "failed"
	SourceDiscoveryCanceled  SourceDiscoveryStatus = "canceled"
)

// SourceDiscovery is one explicit, non-daily discovery generation. It binds
// execution to the Company and Recipe facts that the operator approved.
type SourceDiscovery struct {
	DiscoveryID       string                `json:"discovery_id"`
	WorkID            string                `json:"work_id"`
	CompanyID         string                `json:"company_id"`
	Generation        uint64                `json:"generation"`
	CompanyVersion    uint64                `json:"company_version"`
	WebsiteRevisionID string                `json:"website_revision_id,omitempty"`
	SeedURL           string                `json:"seed_url"`
	RecipeID          string                `json:"recipe_id"`
	RecipeVersion     uint64                `json:"recipe_version"`
	RecipeContentHash string                `json:"recipe_content_hash"`
	ContractHash      string                `json:"contract_hash"`
	Execution         RecipeExecution       `json:"execution"`
	CandidateCount    int                   `json:"candidate_count"`
	NextChunkSequence uint64                `json:"next_chunk_sequence"`
	Status            SourceDiscoveryStatus `json:"status"`
	Version           uint64                `json:"version"`
}

// BindWebsiteRevision records why a new non-daily discovery generation exists.
// The Repository additionally proves that this revision is still the Company's
// current website head in the same transaction that creates the discovery.
func (d SourceDiscovery) BindWebsiteRevision(company Company, revision CompanyWebsiteRevision) (SourceDiscovery, error) {
	if d.Version != 1 || d.Status != SourceDiscoveryQueued || d.WebsiteRevisionID != "" ||
		company.CompanyID != d.CompanyID || company.Version != d.CompanyVersion || company.Website != d.SeedURL ||
		revision.CompanyID != company.CompanyID || revision.Website != company.Website ||
		revision.ConfigurationVersion != company.ConfigurationVersion {
		return SourceDiscovery{}, fmt.Errorf("source discovery website revision must match its frozen Company website")
	}
	if err := revision.Validate(); err != nil {
		return SourceDiscovery{}, err
	}
	d.WebsiteRevisionID = revision.RevisionID
	return d, nil
}

type SourceCandidateDisposition string

const (
	SourceCandidatePending      SourceCandidateDisposition = "pending"
	SourceCandidateAccepted     SourceCandidateDisposition = "accepted"
	SourceCandidateRejected     SourceCandidateDisposition = "rejected"
	SourceCandidateWaitingHuman SourceCandidateDisposition = "waiting_human"
)

type SourceDiscoveryCandidate struct {
	CandidateID        string                     `json:"candidate_id"`
	CandidateKey       string                     `json:"candidate_key"`
	Endpoint           string                     `json:"endpoint"`
	Category           string                     `json:"category,omitempty"`
	FinalURL           string                     `json:"final_url"`
	ConfidenceBasis    string                     `json:"confidence_basis"`
	EvidenceArtifactID string                     `json:"evidence_artifact_id"`
	Disposition        SourceCandidateDisposition `json:"disposition"`
	SourceID           string                     `json:"source_id,omitempty"`
	DecisionActorID    string                     `json:"decision_actor_id,omitempty"`
	DecisionReason     string                     `json:"decision_reason,omitempty"`
	Version            uint64                     `json:"version"`
}

func NewSourceDiscovery(id, workID string, company Company, generation uint64, seedURL string, recipe Recipe) (SourceDiscovery, error) {
	id, workID, seedURL = strings.TrimSpace(id), strings.TrimSpace(workID), strings.TrimSpace(seedURL)
	if id == "" || workID == "" || company.CompanyID == "" || company.Version == 0 || generation == 0 ||
		recipe.Kind != RecipeDiscovery || recipe.Status != RecipeActive || recipe.Version == 0 || recipe.StateVersion == 0 {
		return SourceDiscovery{}, fmt.Errorf("source discovery requires identity, Company version, generation, and active discovery Recipe")
	}
	canonical, err := CanonicalHTTPURL(seedURL)
	if err != nil {
		return SourceDiscovery{}, fmt.Errorf("source discovery seed: %w", err)
	}
	if err := recipe.Validate(); err != nil {
		return SourceDiscovery{}, err
	}
	return SourceDiscovery{DiscoveryID: id, WorkID: workID, CompanyID: company.CompanyID, Generation: generation,
		CompanyVersion: company.Version, SeedURL: canonical, RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version,
		RecipeContentHash: recipe.ContentHash, ContractHash: recipe.ContractHash, Execution: recipe.Execution,
		Status: SourceDiscoveryQueued, Version: 1}, nil
}

func (d SourceDiscovery) Start(expected uint64) (SourceDiscovery, error) {
	if err := requireVersion(expected, d.Version); err != nil {
		return SourceDiscovery{}, err
	}
	if d.Status != SourceDiscoveryQueued {
		return SourceDiscovery{}, &InvalidTransitionError{Entity: "source_discovery", From: string(d.Status), Action: "start"}
	}
	d.Status, d.Version = SourceDiscoveryRunning, d.Version+1
	return d, nil
}

func (d SourceDiscovery) Complete(expected uint64, candidateCount int) (SourceDiscovery, error) {
	if err := requireVersion(expected, d.Version); err != nil {
		return SourceDiscovery{}, err
	}
	if d.Status != SourceDiscoveryRunning || candidateCount != d.CandidateCount || candidateCount < 0 || candidateCount > 10_000 {
		return SourceDiscovery{}, fmt.Errorf("running source discovery and candidate count in [0,10000] are required")
	}
	d.Status, d.CandidateCount, d.Version = SourceDiscoveryCompleted, candidateCount, d.Version+1
	return d, nil
}

func (d SourceDiscovery) Cancel(expected uint64) (SourceDiscovery, error) {
	if err := requireVersion(expected, d.Version); err != nil {
		return SourceDiscovery{}, err
	}
	if d.Status != SourceDiscoveryQueued && d.Status != SourceDiscoveryRunning {
		return SourceDiscovery{}, &InvalidTransitionError{Entity: "source_discovery", From: string(d.Status), Action: "cancel"}
	}
	d.Status, d.Version = SourceDiscoveryCanceled, d.Version+1
	return d, nil
}

func (d SourceDiscovery) AppendCandidates(expected, sequence uint64, count int) (SourceDiscovery, error) {
	if err := requireVersion(expected, d.Version); err != nil {
		return SourceDiscovery{}, err
	}
	if d.Status != SourceDiscoveryRunning || sequence != d.NextChunkSequence || count < 1 || count > 500 ||
		d.CandidateCount+count > 10_000 {
		return SourceDiscovery{}, fmt.Errorf("running discovery, next sequence, and bounded candidate chunk are required")
	}
	d.CandidateCount += count
	d.NextChunkSequence++
	d.Version++
	return d, nil
}

func NewSourceDiscoveryCandidate(endpoint, category, finalURL, confidenceBasis, evidenceArtifactID string) (SourceDiscoveryCandidate, error) {
	endpoint, category = strings.TrimSpace(endpoint), strings.TrimSpace(category)
	finalURL, confidenceBasis = strings.TrimSpace(finalURL), strings.TrimSpace(confidenceBasis)
	evidenceArtifactID = strings.TrimSpace(evidenceArtifactID)
	canonicalEndpoint, err := CanonicalHTTPURL(endpoint)
	if err != nil {
		return SourceDiscoveryCandidate{}, err
	}
	canonicalFinal, err := CanonicalHTTPURL(finalURL)
	if err != nil {
		return SourceDiscoveryCandidate{}, err
	}
	if confidenceBasis == "" || evidenceArtifactID == "" || len(confidenceBasis) > 1024 {
		return SourceDiscoveryCandidate{}, fmt.Errorf("source candidate requires bounded confidence basis and evidence Artifact")
	}
	key, err := CanonicalSourceKey(canonicalFinal, category)
	if err != nil {
		return SourceDiscoveryCandidate{}, err
	}
	sum := sha256.Sum256([]byte(key))
	return SourceDiscoveryCandidate{CandidateID: hex.EncodeToString(sum[:]), CandidateKey: key, Endpoint: canonicalEndpoint, Category: category,
		FinalURL: canonicalFinal, ConfidenceBasis: confidenceBasis, EvidenceArtifactID: evidenceArtifactID,
		Disposition: SourceCandidatePending, Version: 1}, nil
}

func SourceDiscoveryCandidateAggregateID(discoveryID, candidateID string) (string, error) {
	discoveryID, candidateID = strings.TrimSpace(discoveryID), strings.TrimSpace(candidateID)
	if discoveryID == "" || candidateID == "" {
		return "", fmt.Errorf("source discovery and candidate identity are required")
	}
	sum := sha256.Sum256([]byte("recruiting.source.discovery.candidate.v1\n" + discoveryID + "\n" + candidateID))
	return "source-candidate-" + hex.EncodeToString(sum[:16]), nil
}

func (c SourceDiscoveryCandidate) Accept(expected uint64, sourceID, actorID, reason string) (SourceDiscoveryCandidate, error) {
	return c.decide(expected, SourceCandidateAccepted, sourceID, actorID, reason)
}

func (c SourceDiscoveryCandidate) Reject(expected uint64, actorID, reason string) (SourceDiscoveryCandidate, error) {
	return c.decide(expected, SourceCandidateRejected, "", actorID, reason)
}

func (c SourceDiscoveryCandidate) decide(expected uint64, disposition SourceCandidateDisposition, sourceID, actorID, reason string) (SourceDiscoveryCandidate, error) {
	if err := requireVersion(expected, c.Version); err != nil {
		return SourceDiscoveryCandidate{}, err
	}
	sourceID, actorID, reason = strings.TrimSpace(sourceID), strings.TrimSpace(actorID), strings.TrimSpace(reason)
	if c.Disposition != SourceCandidatePending || actorID == "" || reason == "" ||
		(disposition == SourceCandidateAccepted && sourceID == "") {
		return SourceDiscoveryCandidate{}, fmt.Errorf("pending candidate and explicit authenticated decision are required")
	}
	c.Disposition, c.SourceID = disposition, sourceID
	c.DecisionActorID, c.DecisionReason, c.Version = actorID, reason, c.Version+1
	return c, nil
}
