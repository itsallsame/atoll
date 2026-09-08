package recruitingexecutor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/protocol/access"
	"github.com/wanpengxie/atoll/protocol/resource"
)

type companyImportOptions struct {
	Artifact  artifactSinkConfig
	MaxBytes  int64
	ChunkSize int
}

func executeCompanyImportOffer(ctx context.Context, control executionControl, resources executionResourceAccess,
	offer executioncontract.Offer, options companyImportOptions) error {
	if ctx == nil || control == nil || resources == nil || offer.Kind != "company_import" || offer.CompanyImport == nil ||
		offer.Work.Purpose != "company_import" || offer.Attempt.BatchVersion != offer.CompanyImport.Version ||
		offer.RequestedCapability != "company.import" || offer.Budget.PermitID != "" || offer.BudgetExpiresAt != "" ||
		options.MaxBytes < 1 || options.ChunkSize < 1 || options.ChunkSize > 500 {
		return errors.New("company import execution requires an immutable local batch offer and bounded options")
	}
	sinkConfig := options.Artifact
	sinkConfig.WorkID, sinkConfig.AttemptID = offer.Work.WorkID, offer.Attempt.AttemptID
	sink, err := newAtollArtifactSink(resources, sinkConfig)
	if err != nil {
		return fmt.Errorf("prepare company import failure Artifact sink: %w", err)
	}
	if err := control.Accept(ctx, offer); err != nil {
		return fmt.Errorf("accept company import offer: %w", err)
	}
	if err := control.Started(ctx, offer); err != nil {
		return fmt.Errorf("start company import offer: %w", err)
	}
	content, err := readCompanyImportResource(resources, *offer.CompanyImport, options.MaxBytes)
	if err != nil {
		return failLocalExecution(ctx, control, sink, offer, "contract_violated", "company_import_resource", err)
	}
	items, err := parseCompanyImportCSV(content, offer.CompanyImport.SchemaVersion)
	if err != nil {
		return failLocalExecution(ctx, control, sink, offer, "parse_error", "company_import_parse", err)
	}
	previewHash, err := model.CompanyImportPreviewHash(*offer.CompanyImport, items)
	if err != nil {
		return failLocalExecution(ctx, control, sink, offer, "parse_error", "company_import_validate", err)
	}
	if offer.CompanyImport.ItemCount < 0 || offer.CompanyImport.ItemCount > len(items) {
		return failLocalExecution(ctx, control, sink, offer, "contract_violated", "company_import_resume",
			fmt.Errorf("persisted preview item count %d exceeds parsed input count %d", offer.CompanyImport.ItemCount, len(items)))
	}
	expectedVersion := offer.CompanyImport.Version
	for offset, sequence := offer.CompanyImport.ItemCount, offer.CompanyImport.NextChunkSequence; offset < len(items); offset, sequence = offset+options.ChunkSize, sequence+1 {
		end := offset + options.ChunkSize
		if end > len(items) {
			end = len(items)
		}
		submission := executioncontract.CompanyImportPreviewChunkResult{
			CommandID:  fmt.Sprintf("company-import-chunk-%s-%d", offer.Attempt.AttemptID, sequence),
			ResultKind: "company_import_preview_chunk", AttemptID: offer.Attempt.AttemptID,
			ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ExpectedBatchVersion: expectedVersion,
			ChunkSequence: sequence, Items: append([]model.CompanyImportItem(nil), items[offset:end]...),
		}
		if err := control.Submit(ctx, submission.ResultKind, submission); err != nil {
			return fmt.Errorf("submit company import preview chunk %d: %w", sequence, err)
		}
		expectedVersion++
	}
	completion := executioncontract.CompanyImportPreviewCompletionResult{
		CommandID:  "company-import-preview-complete-" + offer.Attempt.AttemptID,
		ResultKind: "company_import_preview_completion", AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ExpectedBatchVersion: expectedVersion, PreviewHash: previewHash,
	}
	if err := control.Submit(ctx, completion.ResultKind, completion); err != nil {
		return fmt.Errorf("submit company import preview completion: %w", err)
	}
	return nil
}

func executeCompanyImportApplyOffer(ctx context.Context, control executionControl, offer executioncontract.Offer) error {
	if ctx == nil || control == nil || offer.Kind != "company_import_apply" || offer.CompanyImport == nil ||
		offer.Work.Purpose != "company_import_apply" || offer.Work.TargetID != offer.CompanyImport.ImportID ||
		offer.CompanyImport.Status != model.CompanyImportRunning || offer.Attempt.BatchVersion != offer.CompanyImport.Version ||
		len(offer.CompanyImportItems) > 500 {
		return fmt.Errorf("company import apply requires a bounded, running, version-fenced offer")
	}
	seen := make(map[string]struct{}, len(offer.CompanyImportItems))
	for index, value := range offer.CompanyImportItems {
		if value.Version == 0 || value.Item.ItemKey == "" || (index > 0 && value.Ordinal <= offer.CompanyImportItems[index-1].Ordinal) {
			return fmt.Errorf("company import apply items must be versioned and strictly ordered")
		}
		if _, duplicate := seen[value.Item.ItemKey]; duplicate {
			return fmt.Errorf("company import apply item keys must be unique")
		}
		seen[value.Item.ItemKey] = struct{}{}
	}
	if err := control.Accept(ctx, offer); err != nil {
		return fmt.Errorf("accept company import apply offer: %w", err)
	}
	if err := control.Started(ctx, offer); err != nil {
		return fmt.Errorf("start company import apply offer: %w", err)
	}
	result := executioncontract.CompanyImportApplyResult{CommandID: "company-import-apply-" + offer.Attempt.AttemptID,
		ResultKind: "company_import_apply", AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ExpectedBatchVersion: offer.CompanyImport.Version}
	if err := control.Submit(ctx, result.ResultKind, result); err != nil {
		return fmt.Errorf("submit company import apply result: %w", err)
	}
	return nil
}

func readCompanyImportResource(resources executionResourceAccess, batch model.CompanyImport, maxBytes int64) ([]byte, error) {
	if resources == nil || maxBytes < 1 || int64(len(batch.InputArtifactRef)) > maxBytes {
		return nil, errors.New("company import Resource reader and byte limit are required")
	}
	reference, err := url.Parse(batch.InputArtifactRef)
	if err != nil || (reference.Scheme != "artifact" && reference.Scheme != "daemon") || reference.Host == "" ||
		reference.User != nil || reference.RawQuery != "" || reference.Fragment != "" {
		return nil, errors.New("company import input must be an opaque Artifact Resource reference")
	}
	var content []byte
	file, openOutcome, openErr := resources.Open(resource.ResourceID(batch.InputArtifactRef), access.OpRead)
	if openErr == nil && openOutcome.Accepted() {
		if reader, readable := file.Reader(); readable {
			defer reader.Close()
			content, err = io.ReadAll(io.LimitReader(reader, maxBytes+1))
			if err != nil {
				return nil, fmt.Errorf("stream company import File Resource: %w", err)
			}
		}
	}
	if content == nil {
		outcome, readErr := resources.Read(resource.ResourceID(batch.InputArtifactRef))
		if readErr != nil {
			return nil, fmt.Errorf("read company import Resource: %w", readErr)
		}
		if !outcome.Accepted() || !outcome.Found {
			return nil, fmt.Errorf("company import Resource unavailable: %s", outcome.RejectReason)
		}
		content = outcome.Value
	}
	if len(content) == 0 || int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("company import Resource size must be in [1,%d] bytes", maxBytes)
	}
	sum := sha256.Sum256(content)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != batch.InputArtifactHash {
		return nil, fmt.Errorf("company import Resource hash mismatch: got %s", actual)
	}
	return append([]byte(nil), content...), nil
}

func parseCompanyImportCSV(content []byte, schemaVersion string) ([]model.CompanyImportItem, error) {
	if schemaVersion != "company-import.v1" {
		return nil, fmt.Errorf("unsupported company import schema %q", schemaVersion)
	}
	reader := csv.NewReader(bytes.NewReader(content))
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = false
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read company import header: %w", err)
	}
	if len(header) != 3 {
		return nil, errors.New("company import header must be company_id,name,website")
	}
	header[0] = strings.TrimPrefix(strings.TrimSpace(header[0]), "\ufeff")
	for index := 1; index < len(header); index++ {
		header[index] = strings.TrimSpace(header[index])
	}
	if header[0] != "company_id" || header[1] != "name" || header[2] != "website" {
		return nil, errors.New("company import header must be company_id,name,website")
	}
	seenCompany, seenWebsite := map[string]struct{}{}, map[string]struct{}{}
	items := make([]model.CompanyImportItem, 0, 1024)
	for rowNumber := 2; ; rowNumber++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read company import row %d: %w", rowNumber, readErr)
		}
		if len(items) >= 1_000_000 {
			return nil, errors.New("company import contains more than 1000000 rows")
		}
		item := model.CompanyImportItem{ItemKey: fmt.Sprintf("row-%09d", rowNumber), PreviewDisposition: model.CompanyImportReady}
		if len(record) != 3 {
			item.PreviewDisposition, item.Detail = model.CompanyImportWaitingHuman, "column count does not match company-import.v1"
			items = append(items, item)
			continue
		}
		company, validationErr := model.NewCompany(strings.TrimSpace(record[0]), strings.TrimSpace(record[1]), strings.TrimSpace(record[2]))
		if validationErr != nil {
			item.CompanyID, item.Name, item.Website = strings.TrimSpace(record[0]), strings.TrimSpace(record[1]), strings.TrimSpace(record[2])
			item.PreviewDisposition, item.Detail = model.CompanyImportWaitingHuman, validationErr.Error()
			items = append(items, item)
			continue
		}
		item.CompanyID, item.Name, item.Website = company.CompanyID, company.Name, company.Website
		if _, duplicate := seenCompany[item.CompanyID]; duplicate {
			item.PreviewDisposition, item.Detail = model.CompanyImportSkipped, "duplicate company_id in input"
		} else if item.Website != "" {
			if _, duplicate := seenWebsite[item.Website]; duplicate {
				item.PreviewDisposition, item.Detail = model.CompanyImportSkipped, "duplicate normalized website in input"
			}
		}
		seenCompany[item.CompanyID] = struct{}{}
		if item.Website != "" {
			seenWebsite[item.Website] = struct{}{}
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, errors.New("company import contains no data rows")
	}
	return items, nil
}
