package recruitingexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestExecuteCompanyImportStreamsBoundedVerifiedPreviewChunks(t *testing.T) {
	content := []byte("company_id,name,website\n" +
		"company-1,One,HTTPS://ONE.EXAMPLE:443/\n" +
		"company-2,Two,https://two.example\n" +
		"company-2,Duplicate ID,https://other.example\n" +
		",Missing ID,https://missing.example\n" +
		"company-5,Five,\n")
	sum := sha256.Sum256(content)
	inputHash := "sha256:" + hex.EncodeToString(sum[:])
	work, _ := model.NewWork("import-work-1", "company_set", "import-1", "company_import", "human")
	batch, _ := model.NewCompanyImport("import-1", work.WorkID, "artifact://imports/companies.csv", inputHash, "company-import.v1", 1)
	attempt, _ := model.NewAttempt("import-attempt-1", work)
	attempt, _ = attempt.BindExecutor("tool:import-executor:1", "boot-1", "company.import")
	attempt, _ = attempt.WithBatchFence(batch.Version)
	offer := executioncontract.Offer{Kind: "company_import", Attempt: attempt, Work: work, CompanyImport: &batch,
		RequestedCapability: "company.import"}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: content,
		inputRef: "artifact://imports/companies.csv"}
	control := &executeControlStub{}
	options := companyImportOptions{Artifact: artifactSinkConfig{DeviceName: "worker", ChannelName: "recruiting",
		Directory: "artifacts", AccessScope: "operators", Retention: "30d", MaxBytes: 1 << 20}, MaxBytes: 1 << 20, ChunkSize: 2}
	if err := executeCompanyImportOffer(context.Background(), control, resources, offer, options); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"accept", "started", "submit:company_import_preview_chunk", "submit:company_import_preview_chunk",
		"submit:company_import_preview_chunk", "submit:company_import_preview_completion"}
	if strings.Join(control.calls, ",") != strings.Join(wantCalls, ",") || len(control.submissions) != 4 {
		t.Fatalf("control lifecycle=%v submissions=%d", control.calls, len(control.submissions))
	}
	first := control.submissions[0].(executioncontract.CompanyImportPreviewChunkResult)
	second := control.submissions[1].(executioncontract.CompanyImportPreviewChunkResult)
	third := control.submissions[2].(executioncontract.CompanyImportPreviewChunkResult)
	completion := control.submissions[3].(executioncontract.CompanyImportPreviewCompletionResult)
	if first.ExpectedBatchVersion != 1 || first.ChunkSequence != 0 || len(first.Items) != 2 || first.Items[0].Website != "https://one.example" ||
		second.ExpectedBatchVersion != 2 || second.Items[0].PreviewDisposition != model.CompanyImportSkipped ||
		second.Items[1].PreviewDisposition != model.CompanyImportWaitingHuman || third.ExpectedBatchVersion != 3 || len(third.Items) != 1 ||
		completion.ExpectedBatchVersion != 4 || !strings.HasPrefix(completion.PreviewHash, "sha256:") {
		t.Fatalf("submissions first=%+v second=%+v third=%+v completion=%+v", first, second, third, completion)
	}
}

func TestCompanyImportRejectsChangedResourceBeforeSubmittingRows(t *testing.T) {
	content := []byte("company_id,name,website\ncompany-1,One,https://one.example\n")
	work, _ := model.NewWork("import-work-hash", "company_set", "import-hash", "company_import", "human")
	batch, _ := model.NewCompanyImport("import-hash", work.WorkID, "artifact://imports/changed.csv",
		"sha256:"+strings.Repeat("a", 64), "company-import.v1", 1)
	attempt, _ := model.NewAttempt("import-attempt-hash", work)
	attempt, _ = attempt.BindExecutor("tool:import-executor:1", "boot-1", "company.import")
	attempt, _ = attempt.WithBatchFence(batch.Version)
	offer := executioncontract.Offer{Kind: "company_import", Attempt: attempt, Work: work, CompanyImport: &batch,
		RequestedCapability: "company.import"}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: content,
		inputRef: "artifact://imports/changed.csv"}
	control := &executeControlStub{}
	options := companyImportOptions{Artifact: artifactSinkConfig{DeviceName: "worker", ChannelName: "recruiting",
		Directory: "artifacts", AccessScope: "operators", Retention: "30d", MaxBytes: 1 << 20}, MaxBytes: 1 << 20, ChunkSize: 100}
	if err := executeCompanyImportOffer(context.Background(), control, resources, offer, options); err != nil {
		t.Fatal(err)
	}
	if len(control.submissions) != 0 || len(control.calls) != 3 || control.calls[2] != "failed" || control.failed.Class != "contract_violated" {
		t.Fatalf("changed Resource was not failed closed: calls=%v submissions=%v report=%+v", control.calls, control.submissions, control.failed)
	}
}

func TestExecuteCompanyImportResumesFromPersistedItemCountAcrossChunkSizeChange(t *testing.T) {
	content := []byte("company_id,name,website\ncompany-1,One,\ncompany-2,Two,\ncompany-3,Three,\n")
	sum := sha256.Sum256(content)
	work, _ := model.NewWork("import-work-resume", "company_set", "import-resume", "company_import", "human")
	batch, _ := model.NewCompanyImport("import-resume", work.WorkID, "artifact://imports/resume.csv",
		"sha256:"+hex.EncodeToString(sum[:]), "company-import.v1", 1)
	batch, _ = batch.AppendPreviewChunk(batch.Version, 0, 2)
	attempt, _ := model.NewAttempt("import-attempt-resume", work)
	attempt, _ = attempt.BindExecutor("tool:import-executor:1", "boot-resume", "company.import")
	attempt, _ = attempt.WithBatchFence(batch.Version)
	offer := executioncontract.Offer{Kind: "company_import", Attempt: attempt, Work: work, CompanyImport: &batch,
		RequestedCapability: "company.import"}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: content,
		inputRef: "artifact://imports/resume.csv"}
	control := &executeControlStub{}
	options := companyImportOptions{Artifact: artifactSinkConfig{DeviceName: "worker", ChannelName: "recruiting",
		Directory: "artifacts", AccessScope: "operators", Retention: "30d", MaxBytes: 1 << 20}, MaxBytes: 1 << 20, ChunkSize: 1}
	if err := executeCompanyImportOffer(context.Background(), control, resources, offer, options); err != nil {
		t.Fatal(err)
	}
	if len(control.submissions) != 2 {
		t.Fatalf("resume submitted %d envelopes, want 2? expected one remaining chunk and completion", len(control.submissions))
	}
	chunk := control.submissions[0].(executioncontract.CompanyImportPreviewChunkResult)
	completion := control.submissions[1].(executioncontract.CompanyImportPreviewCompletionResult)
	if chunk.ChunkSequence != 1 || chunk.ExpectedBatchVersion != batch.Version || len(chunk.Items) != 1 || chunk.Items[0].CompanyID != "company-3" ||
		completion.ExpectedBatchVersion != batch.Version+1 {
		t.Fatalf("resume chunk=%+v completion=%+v", chunk, completion)
	}
}

func TestExecuteCompanyImportApplyAcknowledgesExactBoundedOffer(t *testing.T) {
	parent, _ := model.NewWork("import-parent-apply", "company_set", "import-apply", "company_import", "human")
	parent, _ = parent.Start(parent.Version)
	work, _ := model.NewChildWork(parent, "import-apply-work-1", "company_import", "import-apply", "company_import_apply", "parent")
	batch, _ := model.NewCompanyImport("import-apply", parent.WorkID, "artifact://imports/apply.csv",
		"sha256:"+strings.Repeat("a", 64), "company-import.v1", 1)
	batch.Status, batch.PreviewHash, batch.ItemCount, batch.Version = model.CompanyImportRunning, "sha256:"+strings.Repeat("b", 64), 2, 5
	attempt, _ := model.NewAttempt("import-apply-attempt-1", work)
	attempt, _ = attempt.BindExecutor("tool:import-executor:1", "boot-apply", "company.import")
	attempt, _ = attempt.WithBatchFence(batch.Version)
	offer := executioncontract.Offer{Kind: "company_import_apply", Attempt: attempt, Work: work, CompanyImport: &batch,
		CompanyImportItems: []executioncontract.CompanyImportApplyItem{
			{Ordinal: 0, Version: 1, Item: model.CompanyImportItem{ItemKey: "row-1", CompanyID: "company-1", Name: "One"}},
			{Ordinal: 1, Version: 1, Item: model.CompanyImportItem{ItemKey: "row-2", PreviewDisposition: model.CompanyImportSkipped, Detail: "duplicate"}},
		}}
	control := &executeControlStub{}
	if err := executeCompanyImportApplyOffer(context.Background(), control, offer); err != nil {
		t.Fatal(err)
	}
	if strings.Join(control.calls, ",") != "accept,started,submit:company_import_apply" || len(control.submissions) != 1 {
		t.Fatalf("apply lifecycle=%v submissions=%d", control.calls, len(control.submissions))
	}
	result := control.submissions[0].(executioncontract.CompanyImportApplyResult)
	if result.AttemptID != attempt.AttemptID || result.ExpectedBatchVersion != batch.Version || result.ResultKind != "company_import_apply" {
		t.Fatalf("apply result=%+v", result)
	}
	finalizer := offer
	finalizer.CompanyImportItems = nil
	finalizerControl := &executeControlStub{}
	if err := executeCompanyImportApplyOffer(context.Background(), finalizerControl, finalizer); err != nil {
		t.Fatalf("empty recovered finalizer offer: %v", err)
	}
}
