// Package executioncontract defines the recruiting control-plane/executor
// wire contract. It is an application contract: Atoll transports the payload
// without knowing or changing any recruiting semantics.
package executioncontract

import (
	"encoding/json"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

const Version = "recruiting.execution.v1"

const (
	TypeOffer   = "recruiting.execution.offer"
	TypeAccept  = "recruiting.execution.accept"
	TypeStarted = "recruiting.execution.started"
	TypeFailed  = "recruiting.execution.failed"
	TypeResult  = "recruiting.execution.result"
)

type OfferRequest struct {
	CommandID           string `json:"command_id"`
	ExecutorIncarnation string `json:"executor_incarnation"`
	Capability          string `json:"capability"`
	Origin              string `json:"origin,omitempty"`
	ProfileID           string `json:"profile_id,omitempty"`
}

type TransitionRequest struct {
	CommandID           string `json:"command_id"`
	AttemptID           string `json:"attempt_id"`
	ExecutorIncarnation string `json:"executor_incarnation"`
	Reason              string `json:"reason,omitempty"`
}

// Offer is the immutable input accepted by one executor. Kind selects the
// domain payload while Work/Attempt and routing stay uniform, so listing and
// detail execution remain one executor class.
type Offer struct {
	Kind                string                       `json:"kind"`
	Attempt             model.Attempt                `json:"attempt"`
	Work                model.Work                   `json:"work"`
	Occurrence          *model.SourceOccurrence      `json:"occurrence,omitempty"`
	Checkpoint          *model.IncrementalCheckpoint `json:"checkpoint,omitempty"`
	Detail              *DetailInput                 `json:"detail,omitempty"`
	Budget              model.BudgetPermit           `json:"budget"`
	BudgetExpiresAt     string                       `json:"budget_expires_at"`
	RequestedCapability string                       `json:"requested_capability"`
	RequestedOrigin     string                       `json:"requested_origin,omitempty"`
	RequestedProfileID  string                       `json:"requested_profile_id,omitempty"`
}

type DetailInput struct {
	Job        model.SourceJob              `json:"job"`
	Assignment model.SourceRecipeAssignment `json:"assignment"`
	Recipe     model.Recipe                 `json:"recipe"`
}

// OfferResponse is the completed response body after Atoll merges its core
// response status. Failed responses use Atoll's ordinary error_code/detail
// shape and are decoded separately by the caller.
type OfferResponse struct {
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	ContractVersion string `json:"contract_version"`
	CorrelationID   string `json:"correlation_id"`
	RequestedBy     string `json:"requested_by"`
	Available       bool   `json:"available,omitempty"`
	Offer           *Offer `json:"offer,omitempty"`
}

type TransitionResponse struct {
	Status          string         `json:"status"`
	Reason          string         `json:"reason,omitempty"`
	ContractVersion string         `json:"contract_version"`
	CorrelationID   string         `json:"correlation_id"`
	RequestedBy     string         `json:"requested_by"`
	Attempt         *model.Attempt `json:"attempt,omitempty"`
}

type ListingPageResult struct {
	CommandID           string                     `json:"command_id"`
	ResultKind          string                     `json:"result_kind"`
	AttemptID           string                     `json:"attempt_id"`
	ExecutorIncarnation string                     `json:"executor_incarnation"`
	PageSequence        uint64                     `json:"page_sequence"`
	ResumeCursor        string                     `json:"resume_cursor,omitempty"`
	Terminal            bool                       `json:"terminal"`
	Artifact            model.ArtifactMetadata     `json:"artifact"`
	Observations        []model.ListingObservation `json:"observations"`
}

type ListingCompletionResult struct {
	CommandID           string                     `json:"command_id"`
	ResultKind          string                     `json:"result_kind"`
	AttemptID           string                     `json:"attempt_id"`
	ExecutorIncarnation string                     `json:"executor_incarnation"`
	Artifact            model.ArtifactMetadata     `json:"artifact"`
	Quality             ListingQuality             `json:"quality"`
	Checkpoint          ListingCheckpointCandidate `json:"checkpoint_candidate"`
}

type ListingQuality struct {
	IdentityComplete        bool `json:"identity_complete"`
	OrderingContractHeld    bool `json:"ordering_contract_held"`
	PaginationStable        bool `json:"pagination_stable"`
	PreviousFrontierReached bool `json:"previous_frontier_reached"`
	OverlapCompleted        bool `json:"overlap_completed"`
	ItemCount               int  `json:"item_count"`
}

type ListingCheckpointCandidate struct {
	FrontierActivityAt string   `json:"frontier_activity_at,omitempty"`
	FrontierJobKeys    []string `json:"frontier_job_keys,omitempty"`
}

type DetailResult struct {
	CommandID             string                 `json:"command_id"`
	ResultKind            string                 `json:"result_kind"`
	AttemptID             string                 `json:"attempt_id"`
	ExecutorIncarnation   string                 `json:"executor_incarnation"`
	Artifact              model.ArtifactMetadata `json:"artifact"`
	DetailVersionID       string                 `json:"detail_version_id"`
	NormalizedContentHash string                 `json:"normalized_content_hash"`
	Detail                json.RawMessage        `json:"detail"`
}
