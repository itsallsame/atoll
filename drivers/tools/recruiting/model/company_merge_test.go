package model

import "testing"

func TestCompanyMergePreviewSortsAndFencesConfirmation(t *testing.T) {
	preview, err := NewCompanyMergePreview("merge-1", CompanyMergeApply, "canonical", 7, []CompanyMergeMember{
		{CompanyID: "alias-b", CompanyVersion: 3, SourceCount: 2},
		{CompanyID: "alias-a", CompanyVersion: 2, JobCount: 10},
	}, "operator", "deduplicate companies", "2090-01-01T00:00:00Z")
	if err != nil || preview.Members[0].CompanyID != "alias-a" || preview.PreviewHash == "" || preview.Version != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := preview.Confirm(preview.Version, "sha256:wrong", "2090-01-01T00:01:00Z"); err == nil {
		t.Fatal("merge confirmation accepted a different preview hash")
	}
	confirmed, err := preview.Confirm(preview.Version, preview.PreviewHash, "2090-01-01T00:01:00Z")
	if err != nil || confirmed.Status != CompanyMergeConfirmed || confirmed.Version != 2 {
		t.Fatalf("confirmed=%+v err=%v", confirmed, err)
	}
	if _, err := confirmed.Confirm(confirmed.Version, confirmed.PreviewHash, "2090-01-01T00:02:00Z"); err == nil {
		t.Fatal("confirmed merge preview was applied twice")
	}
}
