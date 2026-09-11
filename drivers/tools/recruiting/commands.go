package recruiting

import (
	"fmt"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
)

// Public business words are application vocabulary. They intentionally live
// in the recruiting extension and do not add recruiting messages to Atoll's
// generic protocol.
const (
	TypeCompanyAdd               = "recruiting.company.add"
	TypeCompanyUpdate            = "recruiting.company.update"
	TypeCompanyPause             = "recruiting.company.pause"
	TypeCompanyResume            = "recruiting.company.resume"
	TypeCompanyArchive           = "recruiting.company.archive"
	TypeCompanyDelete            = "recruiting.company.delete"
	TypeCompanyImport            = "recruiting.company.import"
	TypeCompanyImportGet         = "recruiting.company.import.get"
	TypeCompanyImportItems       = "recruiting.company.import.items"
	TypeCompanyImportConfirm     = "recruiting.company.import.confirm"
	TypeCompanyImportCancel      = "recruiting.company.import.cancel"
	TypeCompanyImportItemResolve = "recruiting.company.import.item.resolve"
	TypeCompanyMergePreview      = "recruiting.company.merge.preview"
	TypeCompanyMergeConfirm      = "recruiting.company.merge.confirm"
	TypeCompanyRestore           = "recruiting.company.restore"
	TypeCompanyGet               = "recruiting.company.get"
	TypeCompanyList              = "recruiting.company.list"

	TypeSourceAdd                      = "recruiting.source.add"
	TypeSourceUpdate                   = "recruiting.source.update"
	TypeSourceValidate                 = "recruiting.source.validate"
	TypeSourceValidationPublish        = "recruiting.source.validation.publish"
	TypeSourceValidationReject         = "recruiting.source.validation.reject"
	TypeSourcePause                    = "recruiting.source.pause"
	TypeSourceResume                   = "recruiting.source.resume"
	TypeSourceArchive                  = "recruiting.source.archive"
	TypeSourceRestore                  = "recruiting.source.restore"
	TypeSourceDiscover                 = "recruiting.source.discover"
	TypeSourceDiscoveryGet             = "recruiting.source.discovery.get"
	TypeSourceDiscoveryCandidates      = "recruiting.source.discovery.candidates"
	TypeSourceDiscoveryCandidateAccept = "recruiting.source.discovery.candidate.accept"
	TypeSourceDiscoveryCandidateReject = "recruiting.source.discovery.candidate.reject"
	TypeSourceGet                      = "recruiting.source.get"
	TypeSourceList                     = "recruiting.source.list"
	TypeSourceProfileBind              = "recruiting.source.profile.bind"
	TypeSourceProfileUnbind            = "recruiting.source.profile.unbind"
	TypeBaselineStart                  = "recruiting.baseline.start"

	TypeJobGet           = "recruiting.job.get"
	TypeJobList          = "recruiting.job.list"
	TypeJobCorrect       = "recruiting.job.correct"
	TypeJobCorrectionGet = "recruiting.job.correction.get"

	TypeProfileRegister    = "recruiting.profile.register"
	TypeProfileGet         = "recruiting.profile.get"
	TypeProfileRepairBegin = "recruiting.profile.repair.begin"

	TypeWorkCreate  = "recruiting.work.create"
	TypeWorkGet     = "recruiting.work.get"
	TypeWorkList    = "recruiting.work.list"
	TypeWorkPause   = "recruiting.work.pause"
	TypeWorkResume  = "recruiting.work.resume"
	TypeWorkCorrect = "recruiting.work.correct"
	TypeWorkRetry   = "recruiting.work.retry"
	TypeWorkCancel  = "recruiting.work.cancel"
	TypeWorkResolve = "recruiting.work.resolve"

	TypeRunDiagnostic     = "recruiting.run.diagnostic"
	TypeRunJoinOccurrence = "recruiting.run.join_occurrence"
	TypeRunProduction     = "recruiting.run.production"

	TypeRecipeInspect              = "recruiting.recipe.inspect"
	TypeRecipePropose              = "recruiting.recipe.propose"
	TypeRecipeValidate             = "recruiting.recipe.validate"
	TypeRecipeApprove              = "recruiting.recipe.approve"
	TypeRecipeReject               = "recruiting.recipe.reject"
	TypeRecipeRollout              = "recruiting.recipe.rollout"
	TypeRecipeRolloutBatch         = "recruiting.recipe.rollout.batch"
	TypeRecipeRolloutBatchGet      = "recruiting.recipe.rollout.batch.get"
	TypeRecipeRolloutBatchItems    = "recruiting.recipe.rollout.batch.items"
	TypeRecipeRolloutBatchConfirm  = "recruiting.recipe.rollout.batch.confirm"
	TypeRecipeRolloutBatchResume   = "recruiting.recipe.rollout.batch.resume"
	TypeRecipeRolloutBatchRollback = "recruiting.recipe.rollout.batch.rollback"
	TypeRecipeRolloutBatchCancel   = "recruiting.recipe.rollout.batch.cancel"
	TypeRecipeQuarantine           = "recruiting.recipe.quarantine"
	TypeRecipeRollback             = "recruiting.recipe.rollback"

	TypeExecutionOffer         = executioncontract.TypeOffer
	TypeExecutionAccept        = executioncontract.TypeAccept
	TypeExecutionStarted       = executioncontract.TypeStarted
	TypeExecutionFailed        = executioncontract.TypeFailed
	TypeExecutionWakeCompleted = executioncontract.TypeWakeCompleted

	TypeDailyRunGet               = "recruiting.daily_run.get"
	TypeDailyRunList              = "recruiting.daily_run.list"
	TypeDailyRunSummary           = "recruiting.daily_run.summary"
	TypeDailyRunOccurrenceExclude = "recruiting.daily_run.occurrence.exclude"
	TypeRepairGet                 = "recruiting.repair.get"
	TypeRepairList                = "recruiting.repair.list"
	TypeRepairValidate            = "recruiting.repair.validation.begin"
	TypeRepairResolve             = "recruiting.repair.resolve"
	TypeRepairRecover             = "recruiting.repair.recover"
	TypeJobsSearch                = "recruiting.jobs.search"
	TypeSystemStatus              = "recruiting.system.status"
	TypeSystemReconcile           = "recruiting.system.reconcile"
	TypeCapacityStatus            = "recruiting.capacity.status"
)

type Target struct {
	Type string `json:"target_type"`
	ID   string `json:"target_id"`
}

func (t Target) Validate() error {
	if strings.TrimSpace(t.Type) == "" || strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("target type and ID are required")
	}
	return nil
}

// MutationCommand is the client-controlled part of every mutation. RequestedBy
// is intentionally absent: the application layer injects the authenticated
// actor identity from the Atoll message envelope.
type MutationCommand struct {
	CommandID       string `json:"command_id"`
	Target          Target `json:"target"`
	ExpectedVersion uint64 `json:"expected_version"`
	Reason          string `json:"reason"`
}

func (c MutationCommand) Validate() error {
	if strings.TrimSpace(c.CommandID) == "" || strings.TrimSpace(c.Reason) == "" || c.ExpectedVersion == 0 {
		return fmt.Errorf("command ID, expected version, and reason are required")
	}
	return c.Target.Validate()
}

type CommandContext struct {
	Command     MutationCommand
	RequestedBy string
}

func NewCommandContext(command MutationCommand, envelopeActorID string) (CommandContext, error) {
	if err := command.Validate(); err != nil {
		return CommandContext{}, err
	}
	if strings.TrimSpace(envelopeActorID) == "" {
		return CommandContext{}, fmt.Errorf("authenticated envelope actor is required")
	}
	return CommandContext{Command: command, RequestedBy: strings.TrimSpace(envelopeActorID)}, nil
}

type BatchCommand struct {
	MutationCommand
	InputArtifactHash string            `json:"input_artifact_hash"`
	SchemaVersion     string            `json:"schema_version"`
	PolicyVersion     uint64            `json:"policy_version"`
	PreviewHash       string            `json:"preview_hash,omitempty"`
	ExpectedVersions  map[string]uint64 `json:"expected_versions,omitempty"`
}

func (c BatchCommand) Validate(requirePreview bool) error {
	if err := c.MutationCommand.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.InputArtifactHash) == "" || strings.TrimSpace(c.SchemaVersion) == "" || c.PolicyVersion == 0 {
		return fmt.Errorf("input artifact hash, schema version, and policy version are required")
	}
	if requirePreview && (strings.TrimSpace(c.PreviewHash) == "" || len(c.ExpectedVersions) == 0) {
		return fmt.Errorf("preview hash and expected versions are required")
	}
	for targetID, version := range c.ExpectedVersions {
		if strings.TrimSpace(targetID) == "" || version == 0 {
			return fmt.Errorf("expected versions must contain non-empty targets and positive versions")
		}
	}
	return nil
}

type RunMode string

const (
	RunDiagnostic     RunMode = "diagnostic"
	RunJoinOccurrence RunMode = "join_occurrence"
	RunProduction     RunMode = "production"
)

func (m RunMode) Validate() error {
	switch m {
	case RunDiagnostic, RunJoinOccurrence, RunProduction:
		return nil
	default:
		return fmt.Errorf("unknown run mode %q", m)
	}
}

func (m RunMode) WritesBusinessData() bool { return m != RunDiagnostic }

func (m RunMode) MayAdvanceCheckpoint() bool { return m == RunJoinOccurrence || m == RunProduction }

type BackfillMode string

const (
	BackfillArtifactRecompute BackfillMode = "artifact_recompute"
	BackfillLiveRefetch       BackfillMode = "live_refetch"
)

func (m BackfillMode) Validate() error {
	switch m {
	case BackfillArtifactRecompute, BackfillLiveRefetch:
		return nil
	default:
		return fmt.Errorf("unknown backfill mode %q", m)
	}
}

// Backfill never advances the daily incremental checkpoint, regardless of how
// its input is obtained.
func (m BackfillMode) MayAdvanceCheckpoint() bool { return false }
