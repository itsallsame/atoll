package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

type CompanyImportStatus string

const (
	CompanyImportPreviewing CompanyImportStatus = "previewing"
	CompanyImportPreviewed  CompanyImportStatus = "previewed"
	CompanyImportConfirmed  CompanyImportStatus = "confirmed"
	CompanyImportRunning    CompanyImportStatus = "running"
	CompanyImportCanceling  CompanyImportStatus = "canceling"
	CompanyImportCompleted  CompanyImportStatus = "completed"
	CompanyImportCanceled   CompanyImportStatus = "canceled"
)

// CompanyImport is the durable control-plane aggregate for one immutable
// input Resource. The Resource bytes stay outside messages and MySQL; their
// reference and digest bind preview and confirm to exactly the same input.
type CompanyImport struct {
	ImportID          string              `json:"import_id"`
	ParentWorkID      string              `json:"parent_work_id"`
	InputArtifactRef  string              `json:"input_artifact_ref"`
	InputArtifactHash string              `json:"input_artifact_hash"`
	SchemaVersion     string              `json:"schema_version"`
	PolicyVersion     uint64              `json:"policy_version"`
	PreviewHash       string              `json:"preview_hash,omitempty"`
	ItemCount         int                 `json:"item_count"`
	NextChunkSequence uint64              `json:"next_chunk_sequence"`
	Outcome           BatchOutcome        `json:"outcome"`
	Status            CompanyImportStatus `json:"status"`
	Version           uint64              `json:"version"`
}

type CompanyImportItem struct {
	ItemKey            string                   `json:"item_key"`
	CompanyID          string                   `json:"company_id,omitempty"`
	Name               string                   `json:"name,omitempty"`
	Website            string                   `json:"website,omitempty"`
	PreviewDisposition CompanyImportDisposition `json:"preview_disposition"`
	Detail             string                   `json:"detail,omitempty"`
}

type CompanyImportDisposition string

const (
	CompanyImportReady        CompanyImportDisposition = "ready"
	CompanyImportSkipped      CompanyImportDisposition = "skipped"
	CompanyImportWaitingHuman CompanyImportDisposition = "waiting_human"
)

func NewCompanyImport(importID, parentWorkID, artifactRef, artifactHash, schemaVersion string, policyVersion uint64) (CompanyImport, error) {
	values := []string{importID, parentWorkID, artifactRef, artifactHash, schemaVersion}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return CompanyImport{}, fmt.Errorf("company import identity, parent work, Artifact reference/hash, and schema are required and normalized")
		}
	}
	if policyVersion == 0 || !validCompanyImportResourceRef(artifactRef) || !validSHA256(artifactHash) {
		return CompanyImport{}, fmt.Errorf("company import requires an opaque Artifact Resource, SHA-256 hash, and policy version")
	}
	return CompanyImport{ImportID: importID, ParentWorkID: parentWorkID, InputArtifactRef: artifactRef,
		InputArtifactHash: artifactHash, SchemaVersion: schemaVersion, PolicyVersion: policyVersion,
		Status: CompanyImportPreviewing, Version: 1}, nil
}

func validCompanyImportResourceRef(value string) bool {
	if len(value) > 1024 {
		return false
	}
	reference, err := url.Parse(value)
	return err == nil && (reference.Scheme == "artifact" || reference.Scheme == "daemon") && reference.Host != "" &&
		reference.User == nil && reference.RawQuery == "" && reference.Fragment == ""
}

func (b CompanyImport) RecordPreview(expected uint64, items []CompanyImportItem) (CompanyImport, error) {
	next, err := b.AppendPreviewChunk(expected, 0, len(items))
	if err != nil {
		return CompanyImport{}, err
	}
	hash, err := CompanyImportPreviewHash(b, items)
	if err != nil {
		return CompanyImport{}, err
	}
	return next.FinishPreview(next.Version, hash)
}

// AppendPreviewChunk advances only bounded progress. Replaying or skipping a
// sequence fails its CAS/sequence fence, so an executor can resume from the
// persisted NextChunkSequence without duplicating rows.
func (b CompanyImport) AppendPreviewChunk(expected, sequence uint64, itemCount int) (CompanyImport, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return CompanyImport{}, err
	}
	if b.Status != CompanyImportPreviewing {
		return CompanyImport{}, &InvalidTransitionError{Entity: "company_import", From: string(b.Status), Action: "append preview chunk"}
	}
	if sequence != b.NextChunkSequence || itemCount < 1 || itemCount > 500 {
		return CompanyImport{}, fmt.Errorf("company import preview requires the next sequence and a chunk of 1..500 items")
	}
	b.ItemCount += itemCount
	b.NextChunkSequence++
	b.Version++
	return b, nil
}

func (b CompanyImport) FinishPreview(expected uint64, previewHash string) (CompanyImport, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return CompanyImport{}, err
	}
	if b.Status != CompanyImportPreviewing || b.ItemCount == 0 || !validSHA256(previewHash) {
		return CompanyImport{}, fmt.Errorf("non-empty previewing company import and SHA-256 preview hash are required")
	}
	b.PreviewHash, b.Status = previewHash, CompanyImportPreviewed
	b.Version++
	return b, nil
}

func (b CompanyImport) Confirm(expected uint64, previewHash string) (CompanyImport, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return CompanyImport{}, err
	}
	if b.Status != CompanyImportPreviewed {
		return CompanyImport{}, &InvalidTransitionError{Entity: "company_import", From: string(b.Status), Action: "confirm"}
	}
	if !validSHA256(previewHash) || previewHash != b.PreviewHash {
		return CompanyImport{}, fmt.Errorf("company import preview hash does not match")
	}
	b.Status = CompanyImportConfirmed
	b.Version++
	return b, nil
}

func (b CompanyImport) Start(expected uint64) (CompanyImport, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return CompanyImport{}, err
	}
	if b.Status != CompanyImportConfirmed {
		return CompanyImport{}, &InvalidTransitionError{Entity: "company_import", From: string(b.Status), Action: "start"}
	}
	b.Status = CompanyImportRunning
	b.Version++
	return b, nil
}

func (b CompanyImport) Complete(expected uint64, results []BatchItemResult) (CompanyImport, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return CompanyImport{}, err
	}
	if b.Status != CompanyImportRunning {
		return CompanyImport{}, &InvalidTransitionError{Entity: "company_import", From: string(b.Status), Action: "complete"}
	}
	outcome, err := AggregateBatch(results)
	if err != nil {
		return CompanyImport{}, err
	}
	if outcome.Total != b.ItemCount {
		return CompanyImport{}, fmt.Errorf("company import result count %d does not match preview count %d", outcome.Total, b.ItemCount)
	}
	b.Outcome, b.Status = outcome, CompanyImportCompleted
	b.Version++
	return b, nil
}

func (b CompanyImport) Cancel(expected uint64) (CompanyImport, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return CompanyImport{}, err
	}
	if b.Status == CompanyImportCompleted || b.Status == CompanyImportCanceled {
		return CompanyImport{}, &InvalidTransitionError{Entity: "company_import", From: string(b.Status), Action: "cancel"}
	}
	b.Status = CompanyImportCanceled
	b.Version++
	return b, nil
}

func (b CompanyImport) RequestCancel(expected uint64) (CompanyImport, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return CompanyImport{}, err
	}
	if b.Status == CompanyImportCompleted || b.Status == CompanyImportCanceled || b.Status == CompanyImportCanceling {
		return CompanyImport{}, &InvalidTransitionError{Entity: "company_import", From: string(b.Status), Action: "request cancel"}
	}
	b.Status = CompanyImportCanceling
	b.Version++
	return b, nil
}

func (b CompanyImport) FinishCancel(expected uint64, results []BatchItemResult) (CompanyImport, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return CompanyImport{}, err
	}
	if b.Status != CompanyImportCanceling {
		return CompanyImport{}, &InvalidTransitionError{Entity: "company_import", From: string(b.Status), Action: "finish cancel"}
	}
	outcome, err := AggregateBatch(results)
	if err != nil {
		return CompanyImport{}, err
	}
	if outcome.Total != b.ItemCount {
		return CompanyImport{}, fmt.Errorf("company import cancellation result count %d does not match preview count %d", outcome.Total, b.ItemCount)
	}
	b.Outcome, b.Status = outcome, CompanyImportCanceled
	b.Version++
	return b, nil
}

func CompanyImportPreviewHash(batch CompanyImport, items []CompanyImportItem) (string, error) {
	if len(items) == 0 {
		return "", fmt.Errorf("company import preview requires at least one item")
	}
	canonical, err := NormalizeCompanyImportItems(items)
	if err != nil {
		return "", err
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].ItemKey < canonical[j].ItemKey })
	payload, _ := json.Marshal(struct {
		ArtifactHash  string              `json:"artifact_hash"`
		SchemaVersion string              `json:"schema_version"`
		PolicyVersion uint64              `json:"policy_version"`
		Items         []CompanyImportItem `json:"items"`
	}{batch.InputArtifactHash, batch.SchemaVersion, batch.PolicyVersion, canonical})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func NormalizeCompanyImportItems(items []CompanyImportItem) ([]CompanyImportItem, error) {
	canonical := closeCompanyImportItems(items)
	seen := make(map[string]struct{}, len(canonical))
	for index := range canonical {
		item := &canonical[index]
		item.ItemKey, item.CompanyID, item.Name = strings.TrimSpace(item.ItemKey), strings.TrimSpace(item.CompanyID), strings.TrimSpace(item.Name)
		item.Detail = strings.TrimSpace(item.Detail)
		if item.PreviewDisposition == "" {
			item.PreviewDisposition = CompanyImportReady
		}
		if item.ItemKey == "" {
			return nil, fmt.Errorf("company import item key is required")
		}
		if _, duplicate := seen[item.ItemKey]; duplicate {
			return nil, fmt.Errorf("duplicate company import item key %q", item.ItemKey)
		}
		seen[item.ItemKey] = struct{}{}
		switch item.PreviewDisposition {
		case CompanyImportReady:
			if item.CompanyID == "" || item.Name == "" || item.Detail != "" {
				return nil, fmt.Errorf("ready company import item %q requires company identity and no error detail", item.ItemKey)
			}
			website, err := CanonicalHTTPURL(item.Website)
			if err != nil {
				return nil, fmt.Errorf("company import item %q website: %w", item.ItemKey, err)
			}
			item.Website = website
		case CompanyImportSkipped, CompanyImportWaitingHuman:
			if item.Detail == "" {
				return nil, fmt.Errorf("non-ready company import item %q requires detail", item.ItemKey)
			}
		default:
			return nil, fmt.Errorf("unknown company import preview disposition %q", item.PreviewDisposition)
		}
	}
	return canonical, nil
}

func closeCompanyImportItems(items []CompanyImportItem) []CompanyImportItem {
	return append([]CompanyImportItem(nil), items...)
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

type BatchItemStatus string

const (
	BatchItemSucceeded    BatchItemStatus = "succeeded"
	BatchItemFailed       BatchItemStatus = "failed"
	BatchItemSkipped      BatchItemStatus = "skipped"
	BatchItemWaitingHuman BatchItemStatus = "waiting_human"
	BatchItemCanceled     BatchItemStatus = "canceled"
)

type BatchItemResult struct {
	ItemKey string          `json:"item_key"`
	Status  BatchItemStatus `json:"status"`
	Detail  string          `json:"detail,omitempty"`
}

type BatchOutcome struct {
	Total        int `json:"total"`
	Succeeded    int `json:"succeeded"`
	Failed       int `json:"failed"`
	Skipped      int `json:"skipped"`
	WaitingHuman int `json:"waiting_human"`
	Canceled     int `json:"canceled"`
}

// CancelBatchChildren models parent cancellation without making the parent a
// transaction boundary. Every non-terminal child is independently canceled,
// which increments its acceptance version and fences any in-flight result.
// Terminal children are retained unchanged.
func CancelBatchChildren(parentWorkID string, children []Work) ([]Work, error) {
	if strings.TrimSpace(parentWorkID) == "" {
		return nil, fmt.Errorf("parent work ID is required")
	}
	seen := make(map[string]struct{}, len(children))
	result := make([]Work, 0, len(children))
	for _, child := range children {
		if child.ParentWorkID != parentWorkID || child.WorkID == "" {
			return nil, fmt.Errorf("work %q is not a child of %q", child.WorkID, parentWorkID)
		}
		if _, duplicate := seen[child.WorkID]; duplicate {
			return nil, fmt.Errorf("duplicate child work %q", child.WorkID)
		}
		seen[child.WorkID] = struct{}{}
		if child.Terminal() {
			result = append(result, child)
			continue
		}
		canceled, err := child.Cancel(child.Version)
		if err != nil {
			return nil, err
		}
		result = append(result, canceled)
	}
	return result, nil
}

func AggregateBatch(results []BatchItemResult) (BatchOutcome, error) {
	seen := map[string]struct{}{}
	out := BatchOutcome{Total: len(results)}
	for _, item := range results {
		if item.ItemKey == "" {
			return BatchOutcome{}, fmt.Errorf("batch item key is required")
		}
		if _, duplicate := seen[item.ItemKey]; duplicate {
			return BatchOutcome{}, fmt.Errorf("duplicate batch item key %q", item.ItemKey)
		}
		seen[item.ItemKey] = struct{}{}
		switch item.Status {
		case BatchItemSucceeded:
			out.Succeeded++
		case BatchItemFailed:
			out.Failed++
		case BatchItemSkipped:
			out.Skipped++
		case BatchItemWaitingHuman:
			out.WaitingHuman++
		case BatchItemCanceled:
			out.Canceled++
		default:
			return BatchOutcome{}, fmt.Errorf("unknown batch item status %q", item.Status)
		}
	}
	return out, nil
}
