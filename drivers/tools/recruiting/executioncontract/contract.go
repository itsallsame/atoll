// Package executioncontract defines the recruiting control-plane/executor
// wire contract. It is an application contract: Atoll transports the payload
// without knowing or changing any recruiting semantics.
package executioncontract

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

const Version = "recruiting.execution.v1"

const (
	TypeOffer         = "recruiting.execution.offer"
	TypeAccept        = "recruiting.execution.accept"
	TypeStarted       = "recruiting.execution.started"
	TypeFailed        = "recruiting.execution.failed"
	TypeResult        = "recruiting.execution.result"
	TypeWake          = "recruiting.execution.wake"
	TypeWakeCompleted = "recruiting.execution.wake.completed"
)

// ValidToolTarget accepts either a stable two-segment Atoll address
// (tool:name) or one concrete seated identity (tool:name:timestamp). Keeping
// placement configurable by stable address avoids a deployment-time cycle
// between control and executor declarations.
func ValidToolTarget(value string) bool {
	if strings.TrimSpace(value) != value || len(value) == 0 || len(value) > 191 {
		return false
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	if parts[0] != "tool" {
		return false
	}
	for _, part := range parts[1:] {
		if part == "" || strings.ContainsAny(part, "\r\n\t ") {
			return false
		}
	}
	return true
}

// TargetMatchesAuthenticatedActor proves that a concrete three-segment
// sender is the member selected by an exact identity or stable two-segment
// target. The authenticated envelope remains authoritative.
func TargetMatchesAuthenticatedActor(target, sender string) bool {
	if !ValidToolTarget(target) || !ValidToolTarget(sender) {
		return false
	}
	senderParts := strings.Split(sender, ":")
	if len(senderParts) != 3 {
		return false
	}
	if target == sender {
		return true
	}
	targetParts := strings.Split(target, ":")
	return len(targetParts) == 2 && targetParts[0] == senderParts[0] && targetParts[1] == senderParts[1]
}

type WakeRequest struct {
	CommandID string `json:"command_id"`
	Origin    string `json:"origin,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
}

type WakeCompletion struct {
	DispatchID string `json:"dispatch_id"`
	DeliveryID string `json:"delivery_id"`
	Status     string `json:"status"`
	AttemptID  string `json:"attempt_id,omitempty"`
	WorkID     string `json:"work_id,omitempty"`
}

type OfferRequest struct {
	CommandID           string `json:"command_id"`
	ExecutorIncarnation string `json:"executor_incarnation"`
	Capability          string `json:"capability"`
	Origin              string `json:"origin,omitempty"`
	ProfileID           string `json:"profile_id,omitempty"`
}

type TransitionRequest struct {
	CommandID           string         `json:"command_id"`
	AttemptID           string         `json:"attempt_id"`
	ExecutorIncarnation string         `json:"executor_incarnation"`
	Reason              string         `json:"reason,omitempty"`
	Failure             *FailureReport `json:"failure,omitempty"`
}

// FailureReport binds classified executor evidence to the same transaction
// that closes execution authority. It remains an application contract; Atoll
// transports it without interpreting recruiting failure classes.
type FailureReport struct {
	Class       string                   `json:"class"`
	Signature   string                   `json:"failure_signature,omitempty"`
	Retryable   bool                     `json:"retryable"`
	NeedsRepair bool                     `json:"needs_repair"`
	Artifact    model.ArtifactMetadata   `json:"artifact"`
	Artifacts   []model.ArtifactMetadata `json:"artifacts,omitempty"`
}

func (f FailureReport) Validate(attemptID string) error {
	if strings.TrimSpace(f.Class) != f.Class {
		return fmt.Errorf("execution failure class is not normalized")
	}
	repairExpected, validClass := failureClassShape(f.Class)
	if !validClass {
		return fmt.Errorf("unsupported execution failure class %q", f.Class)
	}
	if f.NeedsRepair != repairExpected {
		return fmt.Errorf("execution failure repair classification does not match class %q", f.Class)
	}
	if f.Signature != "" && !validFailureSignature(f.Signature) {
		return fmt.Errorf("execution failure signature must be a normalized low-cardinality key")
	}
	validated, err := model.NewArtifactMetadata(f.Artifact.ArtifactID, f.Artifact.Kind, f.Artifact.ContentHash, f.Artifact.ObjectRef,
		f.Artifact.WorkID, f.Artifact.AttemptID, f.Artifact.AccessScope, f.Artifact.Retention, f.Artifact.Redacted)
	if err != nil || validated != f.Artifact || f.Artifact.Kind != model.ArtifactFailure || f.Artifact.AttemptID != strings.TrimSpace(attemptID) {
		return fmt.Errorf("execution failure requires normalized failure Artifact metadata bound to its Attempt")
	}
	seen, primary := map[string]struct{}{}, false
	for _, artifact := range f.EvidenceArtifacts() {
		validated, err := model.NewArtifactMetadata(artifact.ArtifactID, artifact.Kind, artifact.ContentHash, artifact.ObjectRef,
			artifact.WorkID, artifact.AttemptID, artifact.AccessScope, artifact.Retention, artifact.Redacted)
		if err != nil || validated != artifact || artifact.AttemptID != f.Artifact.AttemptID || artifact.WorkID != f.Artifact.WorkID {
			return fmt.Errorf("execution failure supporting Artifacts must be normalized and bound to the same Work and Attempt")
		}
		if _, duplicate := seen[artifact.ArtifactID]; duplicate {
			return fmt.Errorf("execution failure supporting Artifact IDs must be unique")
		}
		seen[artifact.ArtifactID] = struct{}{}
		if artifact == f.Artifact {
			primary = true
		}
	}
	if !primary {
		return fmt.Errorf("execution failure supporting Artifacts must contain the primary failure Artifact")
	}
	return nil
}

// ValidFailureClass exposes the shared closed vocabulary to control-plane
// policy configuration without requiring a fabricated FailureReport.
func ValidFailureClass(class string) bool {
	if strings.TrimSpace(class) != class {
		return false
	}
	_, valid := failureClassShape(class)
	return valid
}

func failureClassShape(class string) (needsRepair bool, valid bool) {
	switch class {
	case "transport_timeout", "endpoint_rejected", "response_too_large", "redirect_rejected", "robots_disallowed", "throttled", "forbidden",
		"upstream_5xx", "unexpected_status", "auth_expired", "captcha", "budget_revoked":
		return false, true
	case "parse_error", "quality_rejected", "contract_violated":
		return true, true
	default:
		return false, false
	}
}

// StableSignature preserves wire compatibility with older executors while
// allowing newer Drivers to distinguish stable failure stages without sending
// raw error text, URLs, or response data into single-flight identities.
func (f FailureReport) StableSignature() string {
	if f.Signature != "" {
		return f.Signature
	}
	return f.Class
}

func validFailureSignature(value string) bool {
	if value == "" || len(value) > 191 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' || character == ':' {
			continue
		}
		return false
	}
	return true
}

// EvidenceArtifacts preserves compatibility with reports created before the
// supporting evidence list was added while giving stores one normalized view.
func (f FailureReport) EvidenceArtifacts() []model.ArtifactMetadata {
	if len(f.Artifacts) == 0 {
		return []model.ArtifactMetadata{f.Artifact}
	}
	return append([]model.ArtifactMetadata(nil), f.Artifacts...)
}

// Offer is the immutable input accepted by one executor. Kind selects the
// domain payload while Work/Attempt and routing stay uniform, so listing and
// detail execution remain one executor class.
type Offer struct {
	Kind                  string                           `json:"kind"`
	Attempt               model.Attempt                    `json:"attempt"`
	Work                  model.Work                       `json:"work"`
	Occurrence            *model.SourceOccurrence          `json:"occurrence,omitempty"`
	ListingRun            *model.ListingRun                `json:"listing_run,omitempty"`
	Baseline              *model.BaselineGeneration        `json:"baseline,omitempty"`
	CompanyImport         *model.CompanyImport             `json:"company_import,omitempty"`
	CompanyImportItems    []CompanyImportApplyItem         `json:"company_import_items,omitempty"`
	Checkpoint            *model.IncrementalCheckpoint     `json:"checkpoint,omitempty"`
	Detail                *DetailInput                     `json:"detail,omitempty"`
	Backfill              *BackfillInput                   `json:"backfill,omitempty"`
	Discovery             *model.SourceDiscovery           `json:"discovery,omitempty"`
	Recipe                *model.Recipe                    `json:"recipe,omitempty"`
	RecipeValidation      *model.RecipeSampleValidation    `json:"recipe_validation,omitempty"`
	ProfileRepair         *model.ProfileRepairSession      `json:"profile_repair,omitempty"`
	ProfileSecurityDomain string                           `json:"profile_security_domain,omitempty"`
	ProfileVerification   *model.ProfileVerificationRecipe `json:"profile_verification_recipe,omitempty"`
	ProfileTaskExpiresAt  string                           `json:"profile_task_expires_at,omitempty"`
	Budget                model.BudgetPermit               `json:"budget"`
	BudgetExpiresAt       string                           `json:"budget_expires_at"`
	RequestedCapability   string                           `json:"requested_capability"`
	RequestedOrigin       string                           `json:"requested_origin,omitempty"`
	RequestedProfileID    string                           `json:"requested_profile_id,omitempty"`
}

// CompanyImportApplyItem is a bounded, immutable slice of the confirmed
// preview. The control plane remains authoritative for the actual mutation;
// the executor only acknowledges this exact envelope. A recovered finalizer
// may receive an empty slice after all item commits but before page closure.
type CompanyImportApplyItem struct {
	Ordinal uint64                  `json:"ordinal"`
	Item    model.CompanyImportItem `json:"item"`
	Version uint64                  `json:"version"`
}

type DetailInput struct {
	Job        model.SourceJob              `json:"job"`
	Assignment model.SourceRecipeAssignment `json:"assignment"`
	Recipe     model.Recipe                 `json:"recipe"`
}

type BackfillInput struct {
	Backfill      model.Backfill          `json:"backfill"`
	Item          model.BackfillItem      `json:"item"`
	Recipe        model.Recipe            `json:"recipe"`
	InputArtifact *model.ArtifactMetadata `json:"input_artifact,omitempty"`
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

// DiagnosticResult is the shared evidence-only envelope used by standalone
// diagnostics and Source validation. ResultKind preserves their distinct
// business meaning; neither carries observations or a checkpoint candidate.
type DiagnosticResult struct {
	CommandID           string                   `json:"command_id"`
	ResultKind          string                   `json:"result_kind"`
	AttemptID           string                   `json:"attempt_id"`
	ExecutorIncarnation string                   `json:"executor_incarnation"`
	Artifacts           []model.ArtifactMetadata `json:"artifacts"`
	Quality             ListingQuality           `json:"quality"`
}

type CompanyImportPreviewChunkResult struct {
	CommandID            string                    `json:"command_id"`
	ResultKind           string                    `json:"result_kind"`
	AttemptID            string                    `json:"attempt_id"`
	ExecutorIncarnation  string                    `json:"executor_incarnation"`
	ExpectedBatchVersion uint64                    `json:"expected_batch_version"`
	ChunkSequence        uint64                    `json:"chunk_sequence"`
	Items                []model.CompanyImportItem `json:"items"`
}

type CompanyImportPreviewCompletionResult struct {
	CommandID            string `json:"command_id"`
	ResultKind           string `json:"result_kind"`
	AttemptID            string `json:"attempt_id"`
	ExecutorIncarnation  string `json:"executor_incarnation"`
	ExpectedBatchVersion uint64 `json:"expected_batch_version"`
	PreviewHash          string `json:"preview_hash"`
}

type CompanyImportApplyResult struct {
	CommandID            string `json:"command_id"`
	ResultKind           string `json:"result_kind"`
	AttemptID            string `json:"attempt_id"`
	ExecutorIncarnation  string `json:"executor_incarnation"`
	ExpectedBatchVersion uint64 `json:"expected_batch_version"`
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

type BackfillResult struct {
	CommandID             string                 `json:"command_id"`
	ResultKind            string                 `json:"result_kind"`
	AttemptID             string                 `json:"attempt_id"`
	ExecutorIncarnation   string                 `json:"executor_incarnation"`
	Artifact              model.ArtifactMetadata `json:"artifact"`
	NormalizedContentHash string                 `json:"normalized_content_hash"`
	Output                json.RawMessage        `json:"output"`
}

// RecipeSampleValidationResult proves that a candidate Recipe executed against
// its frozen sample without carrying normalized business data into production.
type RecipeSampleValidationResult struct {
	CommandID             string                   `json:"command_id"`
	ResultKind            string                   `json:"result_kind"`
	AttemptID             string                   `json:"attempt_id"`
	ExecutorIncarnation   string                   `json:"executor_incarnation"`
	RecipeKind            model.RecipeKind         `json:"recipe_kind"`
	Artifacts             []model.ArtifactMetadata `json:"artifacts"`
	RecordCount           int                      `json:"record_count"`
	ExtractedFieldCount   int                      `json:"extracted_field_count"`
	NormalizedContentHash string                   `json:"normalized_content_hash"`
}

type SourceDiscoveryResult struct {
	CommandID           string                           `json:"command_id"`
	ResultKind          string                           `json:"result_kind"`
	AttemptID           string                           `json:"attempt_id"`
	ExecutorIncarnation string                           `json:"executor_incarnation"`
	Artifact            model.ArtifactMetadata           `json:"artifact"`
	Candidates          []model.SourceDiscoveryCandidate `json:"candidates"`
}

// ProfileRepairSubmission is emitted only by the device-bound executor after
// an operator completed the interactive login. NextSecretRef is an opaque
// local-provider reference, never a cookie, password, or OTP. A separate
// verification Work must still prove that the rotated reference works.
type ProfileRepairSubmission struct {
	CommandID           string                 `json:"command_id"`
	ResultKind          string                 `json:"result_kind"`
	AttemptID           string                 `json:"attempt_id"`
	ExecutorIncarnation string                 `json:"executor_incarnation"`
	SessionID           string                 `json:"session_id"`
	NextSecretRef       string                 `json:"next_secret_ref"`
	Artifact            model.ArtifactMetadata `json:"artifact"`
}

type ProfileVerificationResult struct {
	CommandID           string                 `json:"command_id"`
	ResultKind          string                 `json:"result_kind"`
	AttemptID           string                 `json:"attempt_id"`
	ExecutorIncarnation string                 `json:"executor_incarnation"`
	SessionID           string                 `json:"session_id"`
	SecurityDomain      string                 `json:"security_domain"`
	Authenticated       bool                   `json:"authenticated"`
	Artifact            model.ArtifactMetadata `json:"artifact"`
}

type ResultResponse struct {
	Status              string          `json:"status"`
	Reason              string          `json:"reason,omitempty"`
	ContractVersion     string          `json:"contract_version"`
	CorrelationID       string          `json:"correlation_id"`
	RequestedBy         string          `json:"requested_by"`
	Page                json.RawMessage `json:"page,omitempty"`
	Completion          json.RawMessage `json:"completion,omitempty"`
	Diagnostic          json.RawMessage `json:"diagnostic,omitempty"`
	SourceValidation    json.RawMessage `json:"source_validation,omitempty"`
	RecipeValidation    json.RawMessage `json:"recipe_validation,omitempty"`
	Detail              json.RawMessage `json:"detail,omitempty"`
	Backfill            json.RawMessage `json:"backfill,omitempty"`
	CompanyImport       json.RawMessage `json:"company_import,omitempty"`
	SourceDiscovery     json.RawMessage `json:"source_discovery,omitempty"`
	ProfileRepair       json.RawMessage `json:"profile_repair,omitempty"`
	ProfileVerification json.RawMessage `json:"profile_verification,omitempty"`
}
