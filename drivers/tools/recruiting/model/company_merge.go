package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

type CompanyMergeAction string

const (
	CompanyMergeApply   CompanyMergeAction = "merge"
	CompanyMergeReverse CompanyMergeAction = "reverse"
)

type CompanyMergeStatus string

const (
	CompanyMergePreviewReady CompanyMergeStatus = "ready"
	CompanyMergeConfirmed    CompanyMergeStatus = "confirmed"
)

type CompanyMergeMember struct {
	CompanyID      string `json:"company_id"`
	CompanyVersion uint64 `json:"company_version"`
	AliasVersion   uint64 `json:"alias_version,omitempty"`
	SourceCount    uint64 `json:"source_count"`
	JobCount       uint64 `json:"job_count"`
	OpenWorkCount  uint64 `json:"open_work_count"`
}

type CompanyMergePreview struct {
	MergePreviewID          string               `json:"merge_preview_id"`
	Action                  CompanyMergeAction   `json:"action"`
	CanonicalCompanyID      string               `json:"canonical_company_id"`
	CanonicalCompanyVersion uint64               `json:"canonical_company_version"`
	Members                 []CompanyMergeMember `json:"members"`
	PreviewHash             string               `json:"preview_hash"`
	Status                  CompanyMergeStatus   `json:"status"`
	RequestedBy             string               `json:"requested_by"`
	Reason                  string               `json:"reason"`
	CreatedAt               string               `json:"created_at"`
	ConfirmedAt             string               `json:"confirmed_at,omitempty"`
	Version                 uint64               `json:"version"`
}

func NewCompanyMergePreview(id string, action CompanyMergeAction, canonicalID string, canonicalVersion uint64, members []CompanyMergeMember,
	requestedBy, reason, createdAt string) (CompanyMergePreview, error) {
	id, canonicalID = strings.TrimSpace(id), strings.TrimSpace(canonicalID)
	requestedBy, reason, createdAt = strings.TrimSpace(requestedBy), strings.TrimSpace(reason), strings.TrimSpace(createdAt)
	if id == "" || canonicalID == "" || canonicalVersion == 0 || requestedBy == "" || reason == "" || createdAt == "" {
		return CompanyMergePreview{}, fmt.Errorf("merge preview identity, canonical company, operator, reason, and time are required")
	}
	if action != CompanyMergeApply && action != CompanyMergeReverse {
		return CompanyMergePreview{}, fmt.Errorf("merge action must be merge or reverse")
	}
	if len(members) == 0 || len(members) > 500 {
		return CompanyMergePreview{}, fmt.Errorf("merge preview requires 1 through 500 alias companies")
	}
	copyMembers := append([]CompanyMergeMember(nil), members...)
	sort.Slice(copyMembers, func(i, j int) bool { return copyMembers[i].CompanyID < copyMembers[j].CompanyID })
	for index := range copyMembers {
		member := copyMembers[index]
		if strings.TrimSpace(member.CompanyID) == "" || member.CompanyID == canonicalID || member.CompanyVersion == 0 ||
			(index > 0 && copyMembers[index-1].CompanyID == member.CompanyID) ||
			(action == CompanyMergeReverse && member.AliasVersion == 0) {
			return CompanyMergePreview{}, fmt.Errorf("merge members must be unique non-canonical companies with valid versions")
		}
	}
	preview := CompanyMergePreview{MergePreviewID: id, Action: action, CanonicalCompanyID: canonicalID,
		CanonicalCompanyVersion: canonicalVersion,
		Members:                 copyMembers, Status: CompanyMergePreviewReady, RequestedBy: requestedBy, Reason: reason,
		CreatedAt: createdAt, Version: 1}
	preview.PreviewHash = preview.computeHash()
	return preview, nil
}

func (p CompanyMergePreview) Confirm(expected uint64, previewHash, confirmedAt string) (CompanyMergePreview, error) {
	if err := requireVersion(expected, p.Version); err != nil {
		return CompanyMergePreview{}, err
	}
	if p.Status != CompanyMergePreviewReady || strings.TrimSpace(previewHash) == "" || previewHash != p.PreviewHash ||
		p.PreviewHash != p.computeHash() || strings.TrimSpace(confirmedAt) == "" {
		return CompanyMergePreview{}, &InvalidTransitionError{Entity: "company merge preview", From: string(p.Status), Action: "confirm exact preview"}
	}
	p.Status, p.ConfirmedAt, p.Version = CompanyMergeConfirmed, strings.TrimSpace(confirmedAt), p.Version+1
	return p, nil
}

func (p CompanyMergePreview) computeHash() string {
	var input strings.Builder
	fmt.Fprintf(&input, "company-merge.v1\n%s\n%s\n%d\n", p.Action, p.CanonicalCompanyID, p.CanonicalCompanyVersion)
	for _, member := range p.Members {
		fmt.Fprintf(&input, "%s\x00%d\x00%d\x00%d\x00%d\x00%d\n", member.CompanyID, member.CompanyVersion,
			member.AliasVersion, member.SourceCount, member.JobCount, member.OpenWorkCount)
	}
	sum := sha256.Sum256([]byte(input.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}
