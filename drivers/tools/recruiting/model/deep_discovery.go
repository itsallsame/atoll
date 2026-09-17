package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// DeepDiscoveryStage is the durable research state machine used before a
// deterministic Source Recipe exists. Search and browser implementations are
// sensors; they do not own this lifecycle.
type DeepDiscoveryStage string

const (
	DeepDiscoveryScopeBuilding       DeepDiscoveryStage = "scope_building"
	DeepDiscoveryBrandExpansion      DeepDiscoveryStage = "brand_expansion"
	DeepDiscoverySiteEnumeration     DeepDiscoveryStage = "site_enumeration"
	DeepDiscoverySiteExploration     DeepDiscoveryStage = "site_exploration"
	DeepDiscoveryPoolDetection       DeepDiscoveryStage = "pool_detection"
	DeepDiscoveryCandidateValidation DeepDiscoveryStage = "candidate_validation"
	DeepDiscoveryCoverageReview      DeepDiscoveryStage = "coverage_review"
	DeepDiscoveryCompleted           DeepDiscoveryStage = "completed"
)

type DeepDiscoveryStatus string

const (
	DeepDiscoveryActive       DeepDiscoveryStatus = "active"
	DeepDiscoveryWaitingHuman DeepDiscoveryStatus = "waiting_human"
	DeepDiscoveryDone         DeepDiscoveryStatus = "completed"
	DeepDiscoveryCanceled     DeepDiscoveryStatus = "canceled"
)

type DeepDiscoveryPurpose string

const (
	DeepDiscoveryPurposeCompanySources       DeepDiscoveryPurpose = "company_source_discovery"
	DeepDiscoveryPurposeSourceInitialization DeepDiscoveryPurpose = "source_initialization_evidence"
)

type DiscoveryEvidenceKind string

const (
	EvidenceCompany     DiscoveryEvidenceKind = "company"
	EvidenceBrand       DiscoveryEvidenceKind = "brand"
	EvidenceLegalEntity DiscoveryEvidenceKind = "legal_entity"
	EvidenceDomain      DiscoveryEvidenceKind = "domain"
	EvidenceSite        DiscoveryEvidenceKind = "site"
	EvidenceListingPool DiscoveryEvidenceKind = "listing_pool"
	EvidenceAPIEndpoint DiscoveryEvidenceKind = "api_endpoint"
	EvidenceListURL     DiscoveryEvidenceKind = "list_url"
	EvidenceBlindspot   DiscoveryEvidenceKind = "blindspot"
)

type DiscoveryEvidenceState string

const (
	EvidenceCandidate DiscoveryEvidenceState = "candidate"
	EvidenceValidated DiscoveryEvidenceState = "validated"
	EvidenceRejected  DiscoveryEvidenceState = "rejected"
	EvidenceExcluded  DiscoveryEvidenceState = "excluded"
)

type DiscoverySensor string

const (
	SensorWebSearch     DiscoverySensor = "web_search"
	SensorOfficialSite  DiscoverySensor = "official_site"
	SensorSitemap       DiscoverySensor = "sitemap"
	SensorRobots        DiscoverySensor = "robots"
	SensorBrowser       DiscoverySensor = "browser"
	SensorNetwork       DiscoverySensor = "network"
	SensorATS           DiscoverySensor = "ats_fingerprint"
	SensorDetailReverse DiscoverySensor = "detail_reverse"
	SensorHuman         DiscoverySensor = "human"
)

// RecruitmentURLType describes the audience carried by one independently
// addressable job-list URL. It is deliberately separate from evidence Kind:
// two ListURL nodes can point at the same company while serving different
// recruitment programmes and therefore become different Sources.
type RecruitmentURLType string

const (
	RecruitmentURLSocial  RecruitmentURLType = "social"
	RecruitmentURLCampus  RecruitmentURLType = "campus"
	RecruitmentURLIntern  RecruitmentURLType = "intern"
	RecruitmentURLSpecial RecruitmentURLType = "special"
	RecruitmentURLAll     RecruitmentURLType = "all"
	RecruitmentURLUnknown RecruitmentURLType = "unknown"
)

type DeepDiscoveryBudget struct {
	MaxSearchRounds  int `json:"max_search_rounds"`
	MaxOperations    int `json:"max_operations"`
	SearchRoundsUsed int `json:"search_rounds_used"`
	OperationsUsed   int `json:"operations_used"`
}

type DiscoveryCoverage struct {
	IdentityScoped      bool   `json:"identity_scoped"`
	BrandsReviewed      bool   `json:"brands_reviewed"`
	SitesEnumerated     bool   `json:"sites_enumerated"`
	SitesExplored       bool   `json:"sites_explored"`
	PoolsDetected       bool   `json:"pools_detected"`
	CandidatesValidated bool   `json:"candidates_validated"`
	BlindspotsReviewed  bool   `json:"blindspots_reviewed"`
	CriticalGapCount    int    `json:"critical_gap_count"`
	SocialCoverage      string `json:"social_coverage,omitempty"`
	CampusCoverage      string `json:"campus_coverage,omitempty"`
	InternCoverage      string `json:"intern_coverage,omitempty"`
}

const (
	DiscoveryTypeCovered             = "covered"
	DiscoveryTypeNotFoundAfterSearch = "not_found_after_search"
	DiscoveryTypeExcluded            = "excluded"
)

type DeepDiscoveryMission struct {
	MissionID       string               `json:"mission_id"`
	CompanyID       string               `json:"company_id"`
	Generation      uint64               `json:"discovery_generation"`
	CompanyVersion  uint64               `json:"company_version"`
	CompanyName     string               `json:"company_name"`
	SeedWebsite     string               `json:"seed_website,omitempty"`
	Purpose         DeepDiscoveryPurpose `json:"purpose,omitempty"`
	Stage           DeepDiscoveryStage   `json:"stage"`
	Status          DeepDiscoveryStatus  `json:"status"`
	Budget          DeepDiscoveryBudget  `json:"budget"`
	Coverage        DiscoveryCoverage    `json:"coverage"`
	CheckpointCount uint64               `json:"checkpoint_count"`
	NodeCount       int                  `json:"node_count"`
	EdgeCount       int                  `json:"edge_count"`
	CandidateCount  int                  `json:"candidate_count"`
	WaitingReason   string               `json:"waiting_reason,omitempty"`
	Version         uint64               `json:"version"`
}

type DiscoveryEvidenceNode struct {
	NodeID             string                 `json:"node_id"`
	Kind               DiscoveryEvidenceKind  `json:"kind"`
	CanonicalValue     string                 `json:"canonical_value"`
	Label              string                 `json:"label"`
	State              DiscoveryEvidenceState `json:"state"`
	Sensor             DiscoverySensor        `json:"sensor"`
	EvidenceURL        string                 `json:"evidence_url,omitempty"`
	EvidenceArtifactID string                 `json:"evidence_artifact_id,omitempty"`
	Basis              string                 `json:"basis"`
	RecruitmentType    RecruitmentURLType     `json:"recruitment_type,omitempty"`
	SpecialProgram     string                 `json:"special_program,omitempty"`
	ListProof          *DiscoveryListProof    `json:"list_proof,omitempty"`
}

// DiscoveryListProof is the durable business proof that a URL is a job list,
// rather than a careers landing page or an API endpoint.  It deliberately
// mirrors Snowland's strongest discovery invariant: a list is not validated
// until a real job was followed to a real detail page and the identity-to-URL
// construction was observed.  Artifact ownership is verified by the store.
type DiscoveryListProof struct {
	IsCompanyPage     bool     `json:"is_company_page"`
	IsJobListing      bool     `json:"is_job_listing"`
	HasActivePostings bool     `json:"has_active_postings"`
	ListingArtifactID string   `json:"listing_artifact_id"`
	DetailArtifactID  string   `json:"detail_artifact_id"`
	SampleJobKey      string   `json:"sample_job_key"`
	SampleDetailURL   string   `json:"sample_detail_url"`
	DetailURLPattern  string   `json:"detail_url_pattern"`
	IdentitySource    string   `json:"identity_source"`
	IdentityPath      string   `json:"identity_path"`
	NavigationPath    []string `json:"navigation_path"`
}

func (p DiscoveryListProof) Validate(listURL, evidenceArtifactID string) error {
	listURL, evidenceArtifactID = strings.TrimSpace(listURL), strings.TrimSpace(evidenceArtifactID)
	p.ListingArtifactID, p.DetailArtifactID = strings.TrimSpace(p.ListingArtifactID), strings.TrimSpace(p.DetailArtifactID)
	p.SampleJobKey, p.SampleDetailURL = strings.TrimSpace(p.SampleJobKey), strings.TrimSpace(p.SampleDetailURL)
	p.DetailURLPattern, p.IdentitySource, p.IdentityPath = strings.TrimSpace(p.DetailURLPattern), strings.TrimSpace(p.IdentitySource), strings.TrimSpace(p.IdentityPath)
	if !p.IsCompanyPage || !p.IsJobListing || !p.HasActivePostings || p.ListingArtifactID == "" ||
		p.DetailArtifactID == "" || p.ListingArtifactID != evidenceArtifactID || p.SampleJobKey == "" ||
		p.SampleDetailURL == "" || p.DetailURLPattern == "" || p.IdentityPath == "" ||
		len(p.SampleJobKey) > 512 || len(p.IdentityPath) > 512 || len(p.NavigationPath) < 2 || len(p.NavigationPath) > 20 {
		return fmt.Errorf("validated list URL requires bounded list-to-detail browser proof")
	}
	switch p.IdentitySource {
	case "dom_href", "dom_attribute", "api_field":
	default:
		return fmt.Errorf("validated list URL has an invalid detail identity source")
	}
	canonicalList, err := CanonicalHTTPURL(listURL)
	if err != nil || canonicalList != listURL {
		return fmt.Errorf("validated list URL proof requires a canonical list URL")
	}
	canonicalDetail, err := CanonicalHTTPURL(p.SampleDetailURL)
	if err != nil || canonicalDetail != p.SampleDetailURL || canonicalDetail == canonicalList {
		return fmt.Errorf("validated list URL proof requires a distinct canonical sample detail URL")
	}
	if strings.Count(p.DetailURLPattern, "{value}") != 1 {
		return fmt.Errorf("detail URL pattern must contain exactly one {value}")
	}
	rendered, err := CanonicalHTTPURL(strings.Replace(p.DetailURLPattern, "{value}", p.SampleJobKey, 1))
	if err != nil || rendered != canonicalDetail {
		return fmt.Errorf("detail URL pattern does not reproduce the verified sample detail URL")
	}
	for _, step := range p.NavigationPath {
		if strings.TrimSpace(step) == "" || len(step) > 512 {
			return fmt.Errorf("validated list URL proof contains an invalid navigation step")
		}
	}
	return nil
}

type DiscoveryEvidenceEdge struct {
	EdgeID     string `json:"edge_id"`
	FromNodeID string `json:"from_node_id"`
	ToNodeID   string `json:"to_node_id"`
	Relation   string `json:"relation"`
	Basis      string `json:"basis"`
}

func NewDeepDiscoveryMission(id string, company Company, generation uint64, maxSearchRounds, maxOperations int) (DeepDiscoveryMission, error) {
	return NewDeepDiscoveryMissionForPurpose(id, company, generation, maxSearchRounds, maxOperations,
		DeepDiscoveryPurposeCompanySources)
}

func NewDeepDiscoveryMissionForPurpose(id string, company Company, generation uint64, maxSearchRounds, maxOperations int,
	purpose DeepDiscoveryPurpose) (DeepDiscoveryMission, error) {
	id = strings.TrimSpace(id)
	if id == "" || company.CompanyID == "" || company.Version == 0 || generation == 0 || strings.TrimSpace(company.Name) == "" {
		return DeepDiscoveryMission{}, fmt.Errorf("deep discovery requires mission identity and a versioned Company")
	}
	if purpose != DeepDiscoveryPurposeCompanySources && purpose != DeepDiscoveryPurposeSourceInitialization {
		return DeepDiscoveryMission{}, fmt.Errorf("deep discovery purpose is invalid")
	}
	if maxSearchRounds == 0 {
		maxSearchRounds = 5
	}
	if maxOperations == 0 {
		maxOperations = 250
	}
	if maxSearchRounds < 1 || maxSearchRounds > 20 || maxOperations < 1 || maxOperations > 2_000 {
		return DeepDiscoveryMission{}, fmt.Errorf("deep discovery budget must be bounded")
	}
	return DeepDiscoveryMission{MissionID: id, CompanyID: company.CompanyID, Generation: generation, CompanyVersion: company.Version,
		CompanyName: company.Name, SeedWebsite: company.Website, Purpose: purpose, Stage: DeepDiscoveryScopeBuilding,
		Status: DeepDiscoveryActive, Budget: DeepDiscoveryBudget{MaxSearchRounds: maxSearchRounds,
			MaxOperations: maxOperations}, Version: 1}, nil
}

// IsSourceInitializationEvidence recognizes the explicit purpose and the
// short-lived pre-purpose identity emitted by the first production rollout.
// The compatibility branch can be removed after those Missions are terminal.
func (m DeepDiscoveryMission) IsSourceInitializationEvidence() bool {
	return m.Purpose == DeepDiscoveryPurposeSourceInitialization ||
		(m.Purpose == "" && strings.HasPrefix(m.MissionID, "mission-auto-initialization-"))
}

// CompleteSourceInitializationEvidence closes the bounded evidence-collection
// lifecycle without fabricating the company-discovery coverage graph. The
// repository independently proves that every candidate Source has a completed
// real browser Probe before allowing this transition.
func (m DeepDiscoveryMission) CompleteSourceInitializationEvidence(expected uint64, candidateCount int) (DeepDiscoveryMission, error) {
	if err := requireVersion(expected, m.Version); err != nil {
		return DeepDiscoveryMission{}, err
	}
	if !m.IsSourceInitializationEvidence() || m.Status != DeepDiscoveryActive || candidateCount < 1 {
		return DeepDiscoveryMission{}, fmt.Errorf("active source initialization evidence Mission with candidates is required")
	}
	m.Stage, m.Status, m.WaitingReason, m.CandidateCount, m.Version =
		DeepDiscoveryCompleted, DeepDiscoveryDone, "", candidateCount, m.Version+1
	return m, nil
}

func NewDiscoveryEvidenceNode(kind DiscoveryEvidenceKind, value, label string, state DiscoveryEvidenceState,
	sensor DiscoverySensor, evidenceURL, artifactID, basis string) (DiscoveryEvidenceNode, error) {
	return NewDiscoveryEvidenceNodeWithType(kind, value, label, state, sensor, evidenceURL, artifactID, basis, "", "")
}

func NewDiscoveryEvidenceNodeWithType(kind DiscoveryEvidenceKind, value, label string, state DiscoveryEvidenceState,
	sensor DiscoverySensor, evidenceURL, artifactID, basis string, recruitmentType RecruitmentURLType,
	specialProgram string) (DiscoveryEvidenceNode, error) {
	return NewDiscoveryEvidenceNodeWithTypeAndProof(kind, value, label, state, sensor, evidenceURL, artifactID, basis,
		recruitmentType, specialProgram, nil)
}

func NewDiscoveryEvidenceNodeWithTypeAndProof(kind DiscoveryEvidenceKind, value, label string, state DiscoveryEvidenceState,
	sensor DiscoverySensor, evidenceURL, artifactID, basis string, recruitmentType RecruitmentURLType,
	specialProgram string, listProof *DiscoveryListProof) (DiscoveryEvidenceNode, error) {
	value, label, evidenceURL, artifactID, basis = strings.TrimSpace(value), strings.TrimSpace(label),
		strings.TrimSpace(evidenceURL), strings.TrimSpace(artifactID), strings.TrimSpace(basis)
	specialProgram = strings.TrimSpace(specialProgram)
	if !validEvidenceKind(kind) || value == "" || label == "" || !validEvidenceState(state) || !validSensor(sensor) ||
		basis == "" || len(value) > 2048 || len(label) > 512 || len(basis) > 2048 || (evidenceURL == "" && artifactID == "") {
		return DiscoveryEvidenceNode{}, fmt.Errorf("discovery evidence requires bounded identity, state, sensor, provenance, and basis")
	}
	if evidenceURL != "" {
		canonical, err := CanonicalHTTPURL(evidenceURL)
		if err != nil {
			return DiscoveryEvidenceNode{}, fmt.Errorf("evidence URL: %w", err)
		}
		evidenceURL = canonical
	}
	if kind == EvidenceDomain || kind == EvidenceSite || kind == EvidenceListingPool || kind == EvidenceAPIEndpoint || kind == EvidenceListURL {
		canonical, err := CanonicalHTTPURL(value)
		if err != nil {
			return DiscoveryEvidenceNode{}, fmt.Errorf("evidence value: %w", err)
		}
		value = canonical
	}
	if kind != EvidenceListURL {
		if recruitmentType != "" || specialProgram != "" || listProof != nil {
			return DiscoveryEvidenceNode{}, fmt.Errorf("recruitment type only applies to list URL evidence")
		}
	} else {
		if recruitmentType == "" {
			recruitmentType = RecruitmentURLUnknown
		}
		if !validRecruitmentURLType(recruitmentType) {
			return DiscoveryEvidenceNode{}, fmt.Errorf("unknown recruitment URL type %q", recruitmentType)
		}
		if recruitmentType == RecruitmentURLSpecial {
			if specialProgram == "" || len(specialProgram) > 256 {
				return DiscoveryEvidenceNode{}, fmt.Errorf("special recruitment URL requires a bounded programme name")
			}
		} else if specialProgram != "" {
			return DiscoveryEvidenceNode{}, fmt.Errorf("special programme name requires recruitment type special")
		}
		if state == EvidenceValidated && recruitmentType == RecruitmentURLUnknown {
			return DiscoveryEvidenceNode{}, fmt.Errorf("validated list URL requires an evidence-backed recruitment type")
		}
		if state == EvidenceValidated {
			if listProof == nil {
				return DiscoveryEvidenceNode{}, fmt.Errorf("validated list URL requires real list-to-detail proof")
			}
			if err := listProof.Validate(value, artifactID); err != nil {
				return DiscoveryEvidenceNode{}, err
			}
		} else if listProof != nil {
			return DiscoveryEvidenceNode{}, fmt.Errorf("list-to-detail proof only applies to a validated list URL")
		}
	}
	sum := sha256.Sum256([]byte("recruiting.deep-discovery.node.v1\n" + string(kind) + "\n" + strings.ToLower(value)))
	return DiscoveryEvidenceNode{NodeID: "discovery-node-" + hex.EncodeToString(sum[:16]), Kind: kind,
		CanonicalValue: value, Label: label, State: state, Sensor: sensor, EvidenceURL: evidenceURL,
		EvidenceArtifactID: artifactID, Basis: basis, RecruitmentType: recruitmentType, SpecialProgram: specialProgram,
		ListProof: listProof}, nil
}

func validRecruitmentURLType(value RecruitmentURLType) bool {
	switch value {
	case RecruitmentURLSocial, RecruitmentURLCampus, RecruitmentURLIntern, RecruitmentURLSpecial, RecruitmentURLAll, RecruitmentURLUnknown:
		return true
	default:
		return false
	}
}

// SourceCategory converts an evidence-backed list URL classification into
// the stable Source category consumed by downstream Recipe and run logic.
func (n DiscoveryEvidenceNode) SourceCategory() (string, error) {
	if n.Kind != EvidenceListURL || n.State != EvidenceValidated || !validRecruitmentURLType(n.RecruitmentType) ||
		n.RecruitmentType == RecruitmentURLUnknown {
		return "", fmt.Errorf("validated and classified list URL evidence is required")
	}
	if n.RecruitmentType == RecruitmentURLSpecial {
		program := strings.TrimSpace(n.SpecialProgram)
		if program == "" || len(program) > 256 {
			return "", fmt.Errorf("special recruitment URL requires a bounded programme name")
		}
		return string(n.RecruitmentType) + ":" + program, nil
	}
	if strings.TrimSpace(n.SpecialProgram) != "" {
		return "", fmt.Errorf("special programme name requires recruitment type special")
	}
	return string(n.RecruitmentType), nil
}

func NewDiscoveryEvidenceEdge(from, to, relation, basis string) (DiscoveryEvidenceEdge, error) {
	from, to, relation, basis = strings.TrimSpace(from), strings.TrimSpace(to), strings.TrimSpace(relation), strings.TrimSpace(basis)
	if from == "" || to == "" || from == to || relation == "" || basis == "" || len(relation) > 64 || len(basis) > 1024 {
		return DiscoveryEvidenceEdge{}, fmt.Errorf("discovery evidence edge requires distinct nodes, relation, and basis")
	}
	sum := sha256.Sum256([]byte("recruiting.deep-discovery.edge.v1\n" + from + "\n" + relation + "\n" + to))
	return DiscoveryEvidenceEdge{EdgeID: "discovery-edge-" + hex.EncodeToString(sum[:16]), FromNodeID: from,
		ToNodeID: to, Relation: relation, Basis: basis}, nil
}

func (m DeepDiscoveryMission) Checkpoint(expected uint64, nextStage DeepDiscoveryStage, coverage DiscoveryCoverage,
	searchRounds, operations, addedNodes, addedEdges, candidates int) (DeepDiscoveryMission, error) {
	if err := requireVersion(expected, m.Version); err != nil {
		return DeepDiscoveryMission{}, err
	}
	if m.Status != DeepDiscoveryActive || stageOrdinal(nextStage) < stageOrdinal(m.Stage) || stageOrdinal(nextStage) > stageOrdinal(m.Stage)+1 || nextStage == DeepDiscoveryCompleted {
		return DeepDiscoveryMission{}, &InvalidTransitionError{Entity: "deep_discovery", From: string(m.Stage), Action: "checkpoint " + string(nextStage)}
	}
	if searchRounds < 0 || operations < 0 || addedNodes < 0 || addedEdges < 0 || candidates < 0 || operations < searchRounds ||
		m.Budget.SearchRoundsUsed+searchRounds > m.Budget.MaxSearchRounds || m.Budget.OperationsUsed+operations > m.Budget.MaxOperations {
		return DeepDiscoveryMission{}, fmt.Errorf("deep discovery checkpoint exceeds its frozen budget")
	}
	if err := coverageAllowsStage(nextStage, coverage); err != nil {
		return DeepDiscoveryMission{}, err
	}
	if stageOrdinal(nextStage) >= stageOrdinal(DeepDiscoverySiteEnumeration) &&
		m.Budget.SearchRoundsUsed+searchRounds < 1 {
		return DeepDiscoveryMission{}, fmt.Errorf("deep discovery must perform at least one real search round before site enumeration")
	}
	m.Stage, m.Coverage = nextStage, coverage
	m.Budget.SearchRoundsUsed += searchRounds
	m.Budget.OperationsUsed += operations
	m.NodeCount += addedNodes
	m.EdgeCount += addedEdges
	m.CandidateCount += candidates
	m.CheckpointCount++
	m.WaitingReason = ""
	m.Version++
	return m, nil
}

// ConsumeOperations reserves external sensor work before it is dispatched.
// Checkpoints may account for Agent-local reasoning/search, while executable
// browser/network probes are charged here exactly once by their create
// command and therefore cannot bypass the frozen Mission budget.
func (m DeepDiscoveryMission) ConsumeOperations(expected uint64, operations int) (DeepDiscoveryMission, error) {
	if err := requireVersion(expected, m.Version); err != nil {
		return DeepDiscoveryMission{}, err
	}
	if m.Status != DeepDiscoveryActive || operations < 1 ||
		m.Budget.OperationsUsed+operations > m.Budget.MaxOperations {
		return DeepDiscoveryMission{}, fmt.Errorf("active deep discovery mission with available operation budget is required")
	}
	m.Budget.OperationsUsed += operations
	m.Version++
	return m, nil
}

func (m DeepDiscoveryMission) WaitForHuman(expected uint64, reason string) (DeepDiscoveryMission, error) {
	if err := requireVersion(expected, m.Version); err != nil {
		return DeepDiscoveryMission{}, err
	}
	reason = strings.TrimSpace(reason)
	if m.Status != DeepDiscoveryActive || reason == "" {
		return DeepDiscoveryMission{}, fmt.Errorf("active mission and waiting reason are required")
	}
	m.Status, m.WaitingReason, m.Version = DeepDiscoveryWaitingHuman, reason, m.Version+1
	return m, nil
}

func (m DeepDiscoveryMission) Resume(expected uint64) (DeepDiscoveryMission, error) {
	if err := requireVersion(expected, m.Version); err != nil {
		return DeepDiscoveryMission{}, err
	}
	if m.Status != DeepDiscoveryWaitingHuman {
		return DeepDiscoveryMission{}, &InvalidTransitionError{Entity: "deep_discovery", From: string(m.Status), Action: "resume"}
	}
	m.Status, m.WaitingReason, m.Version = DeepDiscoveryActive, "", m.Version+1
	return m, nil
}

func (m DeepDiscoveryMission) Cancel(expected uint64, reason string) (DeepDiscoveryMission, error) {
	if err := requireVersion(expected, m.Version); err != nil {
		return DeepDiscoveryMission{}, err
	}
	reason = strings.TrimSpace(reason)
	if (m.Status != DeepDiscoveryActive && m.Status != DeepDiscoveryWaitingHuman) || reason == "" {
		return DeepDiscoveryMission{}, fmt.Errorf("open deep discovery mission and cancellation reason are required")
	}
	m.Status, m.WaitingReason, m.Version = DeepDiscoveryCanceled, reason, m.Version+1
	return m, nil
}

func (m DeepDiscoveryMission) Complete(expected uint64) (DeepDiscoveryMission, error) {
	if err := requireVersion(expected, m.Version); err != nil {
		return DeepDiscoveryMission{}, err
	}
	if m.Status != DeepDiscoveryActive || m.Stage != DeepDiscoveryCoverageReview || m.CandidateCount < 1 || m.Coverage.CriticalGapCount != 0 ||
		!m.Coverage.IdentityScoped || !m.Coverage.BrandsReviewed || !m.Coverage.SitesEnumerated || !m.Coverage.SitesExplored ||
		!m.Coverage.PoolsDetected || !m.Coverage.CandidatesValidated || !m.Coverage.BlindspotsReviewed ||
		!validDiscoveryTypeDisposition(m.Coverage.SocialCoverage) || !validDiscoveryTypeDisposition(m.Coverage.CampusCoverage) ||
		!validDiscoveryTypeDisposition(m.Coverage.InternCoverage) {
		return DeepDiscoveryMission{}, fmt.Errorf("deep discovery completion requires reviewed coverage, no critical gaps, and a validated candidate")
	}
	m.Stage, m.Status, m.WaitingReason, m.Version = DeepDiscoveryCompleted, DeepDiscoveryDone, "", m.Version+1
	return m, nil
}

func validDiscoveryTypeDisposition(value string) bool {
	switch value {
	case DiscoveryTypeCovered, DiscoveryTypeNotFoundAfterSearch, DiscoveryTypeExcluded:
		return true
	default:
		return false
	}
}

func stageOrdinal(stage DeepDiscoveryStage) int {
	switch stage {
	case DeepDiscoveryScopeBuilding:
		return 0
	case DeepDiscoveryBrandExpansion:
		return 1
	case DeepDiscoverySiteEnumeration:
		return 2
	case DeepDiscoverySiteExploration:
		return 3
	case DeepDiscoveryPoolDetection:
		return 4
	case DeepDiscoveryCandidateValidation:
		return 5
	case DeepDiscoveryCoverageReview:
		return 6
	case DeepDiscoveryCompleted:
		return 7
	default:
		return -100
	}
}

func coverageAllowsStage(stage DeepDiscoveryStage, c DiscoveryCoverage) error {
	required := []bool{c.IdentityScoped, c.BrandsReviewed, c.SitesEnumerated, c.SitesExplored, c.PoolsDetected, c.CandidatesValidated}
	ordinal := stageOrdinal(stage)
	for i := 0; i < ordinal && i < len(required); i++ {
		if !required[i] {
			return fmt.Errorf("deep discovery stage %s lacks prerequisite coverage", stage)
		}
	}
	if c.CriticalGapCount < 0 {
		return fmt.Errorf("critical gap count cannot be negative")
	}
	if stage == DeepDiscoveryCoverageReview && (!validDiscoveryTypeDisposition(c.SocialCoverage) ||
		!validDiscoveryTypeDisposition(c.CampusCoverage) || !validDiscoveryTypeDisposition(c.InternCoverage)) {
		return fmt.Errorf("coverage review requires an explicit social, campus, and intern disposition")
	}
	return nil
}

func validEvidenceKind(v DiscoveryEvidenceKind) bool { return stageOrdinalForKind(v) >= 0 }
func stageOrdinalForKind(v DiscoveryEvidenceKind) int {
	switch v {
	case EvidenceCompany, EvidenceBrand, EvidenceLegalEntity, EvidenceDomain, EvidenceSite, EvidenceListingPool, EvidenceAPIEndpoint, EvidenceListURL, EvidenceBlindspot:
		return 1
	}
	return -1
}
func validEvidenceState(v DiscoveryEvidenceState) bool {
	return v == EvidenceCandidate || v == EvidenceValidated || v == EvidenceRejected || v == EvidenceExcluded
}
func validSensor(v DiscoverySensor) bool {
	switch v {
	case SensorWebSearch, SensorOfficialSite, SensorSitemap, SensorRobots, SensorBrowser, SensorNetwork, SensorATS, SensorDetailReverse, SensorHuman:
		return true
	}
	return false
}
