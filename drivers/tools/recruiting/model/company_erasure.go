package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type CompanyErasureStatus string

const (
	CompanyErasurePreviewing       CompanyErasureStatus = "previewing"
	CompanyErasureAwaitingApproval CompanyErasureStatus = "awaiting_approval"
	CompanyErasureApproved         CompanyErasureStatus = "approved"
	CompanyErasureErasing          CompanyErasureStatus = "erasing"
	CompanyErasureResourceCleanup  CompanyErasureStatus = "resource_cleanup"
	CompanyErasureCompleted        CompanyErasureStatus = "completed"
	CompanyErasureBlocked          CompanyErasureStatus = "blocked"
)

type CompanyErasureImpact struct {
	Sources           uint64 `json:"sources"`
	Jobs              uint64 `json:"jobs"`
	Works             uint64 `json:"works"`
	Attempts          uint64 `json:"attempts"`
	Artifacts         uint64 `json:"artifacts"`
	ResourceObjects   uint64 `json:"resource_objects"`
	Observations      uint64 `json:"observations"`
	DetailVersions    uint64 `json:"detail_versions"`
	DailyOccurrences  uint64 `json:"daily_occurrences"`
	SourceDiscoveries uint64 `json:"source_discoveries"`
	WebsiteRevisions  uint64 `json:"website_revisions"`
	Overrides         uint64 `json:"overrides"`
	Backfills         uint64 `json:"backfills"`
	BackfillItems     uint64 `json:"backfill_items"`
	ActiveExecutions  uint64 `json:"active_executions"`
}

type CompanyErasureMember struct {
	ErasureID      string `json:"erasure_id"`
	SourceID       string `json:"source_id"`
	SourceVersion  uint64 `json:"source_version"`
	ExecutionFence uint64 `json:"execution_fence"`
}

func NewCompanyErasureMember(erasureID string, source RecruitmentSource) (CompanyErasureMember, error) {
	erasureID = strings.TrimSpace(erasureID)
	if erasureID == "" || source.SourceID == "" || source.Version == 0 || source.ExecutionFence == 0 ||
		source.ControlStatus != ControlArchived {
		return CompanyErasureMember{}, fmt.Errorf("erasure member requires an archived Source with version and execution fence")
	}
	return CompanyErasureMember{ErasureID: erasureID, SourceID: source.SourceID,
		SourceVersion: source.Version, ExecutionFence: source.ExecutionFence}, nil
}

// CompanyErasure is a durable, non-Company-FK control fact. It deliberately
// survives deletion as the minimal proof that an approved scope was erased.
type CompanyErasure struct {
	ErasureID          string               `json:"erasure_id"`
	WorkID             string               `json:"work_id"`
	CompanyID          string               `json:"company_id"`
	CompanyVersion     uint64               `json:"company_version"`
	PolicyVersion      string               `json:"policy_version"`
	Reason             string               `json:"reason"`
	RequestedBy        string               `json:"requested_by"`
	ExecuteAfter       string               `json:"execute_after"`
	Status             CompanyErasureStatus `json:"status"`
	PreviewCursor      string               `json:"preview_cursor,omitempty"`
	SourceCount        uint64               `json:"source_count"`
	PreviewAccumulator string               `json:"preview_accumulator,omitempty"`
	PreviewHash        string               `json:"preview_hash,omitempty"`
	Impact             CompanyErasureImpact `json:"impact"`
	ApprovedBy         string               `json:"approved_by,omitempty"`
	ApprovedAt         string               `json:"approved_at,omitempty"`
	StartedAt          string               `json:"started_at,omitempty"`
	CompletedAt        string               `json:"completed_at,omitempty"`
	BlockedReason      string               `json:"blocked_reason,omitempty"`
	CreatedAt          string               `json:"created_at"`
	Version            uint64               `json:"version"`
}

func NewCompanyErasure(id, workID string, company Company, policyVersion, requestedBy, reason string,
	executeAfter, createdAt time.Time) (CompanyErasure, error) {
	id, workID, policyVersion = strings.TrimSpace(id), strings.TrimSpace(workID), strings.TrimSpace(policyVersion)
	requestedBy, reason = strings.TrimSpace(requestedBy), strings.TrimSpace(reason)
	if id == "" || workID == "" || company.CompanyID == "" || company.Version == 0 || company.ControlStatus != ControlArchived ||
		policyVersion == "" || humanPrincipal(requestedBy) == "" || reason == "" || len(reason) > 2048 ||
		executeAfter.IsZero() || createdAt.IsZero() || executeAfter.Before(createdAt) {
		return CompanyErasure{}, fmt.Errorf("Company erasure requires archived Company, policy, requester, reason, and non-past execution time")
	}
	return CompanyErasure{ErasureID: id, WorkID: workID, CompanyID: company.CompanyID, CompanyVersion: company.Version,
		PolicyVersion: policyVersion, Reason: reason, RequestedBy: requestedBy,
		ExecuteAfter: executeAfter.UTC().Format(time.RFC3339Nano), Status: CompanyErasurePreviewing,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano), Version: 1}, nil
}

func AdvanceCompanyErasurePreviewHash(erasure CompanyErasure, previous string,
	members []CompanyErasureMember) (string, error) {
	previous = strings.TrimSpace(previous)
	if erasure.ErasureID == "" || erasure.WorkID == "" || erasure.CompanyID == "" || erasure.CompanyVersion == 0 ||
		erasure.PolicyVersion == "" || erasure.ExecuteAfter == "" {
		return "", fmt.Errorf("complete Company erasure identity and policy are required")
	}
	h := sha256.New()
	if previous == "" {
		fmt.Fprintf(h, "company-erasure.v1\n%s\n%s\n%s\n%d\n%s\n%s\n%s\n%s\n", erasure.ErasureID,
			erasure.WorkID, erasure.CompanyID, erasure.CompanyVersion, erasure.PolicyVersion, erasure.ExecuteAfter,
			erasure.RequestedBy, erasure.Reason)
	} else {
		if len(previous) != len("sha256:")+64 || !strings.HasPrefix(previous, "sha256:") {
			return "", fmt.Errorf("previous Company erasure accumulator must be SHA-256")
		}
		decoded, err := hex.DecodeString(strings.TrimPrefix(previous, "sha256:"))
		if err != nil || len(decoded) != sha256.Size {
			return "", fmt.Errorf("decode Company erasure accumulator")
		}
		h.Write(decoded)
	}
	last := ""
	for _, member := range members {
		if member.ErasureID != erasure.ErasureID || member.SourceID == "" || member.SourceID <= last ||
			member.SourceVersion == 0 || member.ExecutionFence == 0 {
			return "", fmt.Errorf("Company erasure preview members must be complete and strictly ordered")
		}
		fmt.Fprintf(h, "%s\n%d\n%d\n", member.SourceID, member.SourceVersion, member.ExecutionFence)
		last = member.SourceID
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func (e CompanyErasure) AdvancePreview(expected uint64, members []CompanyErasureMember, nextCursor,
	accumulator string, hasMore bool, finalImpact CompanyErasureImpact) (CompanyErasure, error) {
	if err := requireVersion(expected, e.Version); err != nil {
		return CompanyErasure{}, err
	}
	if e.Status != CompanyErasurePreviewing || len(members) > 500 || strings.TrimSpace(accumulator) == "" ||
		(hasMore && (len(members) == 0 || strings.TrimSpace(nextCursor) == "")) ||
		(!hasMore && strings.TrimSpace(nextCursor) != "") {
		return CompanyErasure{}, fmt.Errorf("previewing Company erasure requires a bounded canonical page")
	}
	if len(members) > 0 {
		if (e.PreviewCursor != "" && members[0].SourceID <= e.PreviewCursor) ||
			(hasMore && strings.TrimSpace(nextCursor) != members[len(members)-1].SourceID) {
			return CompanyErasure{}, fmt.Errorf("Company erasure preview page does not continue its stable cursor")
		}
	}
	calculated, err := AdvanceCompanyErasurePreviewHash(e, e.PreviewAccumulator, members)
	if err != nil || calculated != accumulator {
		return CompanyErasure{}, fmt.Errorf("Company erasure preview accumulator does not match members")
	}
	e.SourceCount += uint64(len(members))
	e.PreviewAccumulator = accumulator
	e.PreviewCursor = strings.TrimSpace(nextCursor)
	if !hasMore {
		if finalImpact.Sources != e.SourceCount || finalImpact.ActiveExecutions != 0 {
			return CompanyErasure{}, fmt.Errorf("Company erasure final impact must match archived members and have no active execution")
		}
		e.Impact, e.PreviewHash = finalImpact, accumulator
		e.Status = CompanyErasureAwaitingApproval
	}
	e.Version++
	return e, nil
}

func (e CompanyErasure) Approve(expected uint64, previewHash, approver string, at time.Time) (CompanyErasure, error) {
	if err := requireVersion(expected, e.Version); err != nil {
		return CompanyErasure{}, err
	}
	approver, previewHash = strings.TrimSpace(approver), strings.TrimSpace(previewHash)
	requesterPrincipal, approverPrincipal := humanPrincipal(e.RequestedBy), humanPrincipal(approver)
	if e.Status != CompanyErasureAwaitingApproval || previewHash == "" || previewHash != e.PreviewHash ||
		requesterPrincipal == "" || approverPrincipal == "" || approverPrincipal == requesterPrincipal || at.IsZero() {
		return CompanyErasure{}, &InvalidTransitionError{Entity: "company erasure", From: string(e.Status), Action: "approve exact preview by a different operator"}
	}
	e.Status, e.ApprovedBy = CompanyErasureApproved, approver
	e.ApprovedAt = at.UTC().Format(time.RFC3339Nano)
	e.Version++
	return e, nil
}

func humanPrincipal(actorID string) string {
	parts := strings.Split(strings.TrimSpace(actorID), ":")
	if len(parts) < 2 || parts[0] != "human" || strings.TrimSpace(parts[1]) == "" {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func (e CompanyErasure) Begin(expected uint64, at time.Time) (CompanyErasure, error) {
	if err := requireVersion(expected, e.Version); err != nil {
		return CompanyErasure{}, err
	}
	executeAfter, err := time.Parse(time.RFC3339Nano, e.ExecuteAfter)
	if err != nil || e.Status != CompanyErasureApproved || at.IsZero() || at.Before(executeAfter) {
		return CompanyErasure{}, &InvalidTransitionError{Entity: "company erasure", From: string(e.Status), Action: "begin after retention time"}
	}
	e.Status, e.StartedAt = CompanyErasureErasing, at.UTC().Format(time.RFC3339Nano)
	e.Version++
	return e, nil
}
