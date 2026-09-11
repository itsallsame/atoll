package model

import (
	"testing"
	"time"
)

func TestCompanyErasureRequiresFrozenPreviewTwoPeopleAndRetention(t *testing.T) {
	now := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	company, _ := NewCompany("erase-company", "Erase", "https://erase.example")
	company, _ = company.Archive(company.Version)
	erasure, err := NewCompanyErasure("erasure-1", "erasure-work-1", company, "policy-1", "human:requester",
		"approved compliance request", now.Add(time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	sourceA, _ := NewRecruitmentSource("source-a", company.CompanyID, "https://erase.example/a", "all", 1)
	sourceA, _ = sourceA.Archive(sourceA.Version)
	sourceB, _ := NewRecruitmentSource("source-b", company.CompanyID, "https://erase.example/b", "all", 1)
	sourceB, _ = sourceB.Archive(sourceB.Version)
	memberA, _ := NewCompanyErasureMember(erasure.ErasureID, sourceA)
	memberB, _ := NewCompanyErasureMember(erasure.ErasureID, sourceB)
	firstHash, _ := AdvanceCompanyErasurePreviewHash(erasure, "", []CompanyErasureMember{memberA})
	erasure, err = erasure.AdvancePreview(erasure.Version, []CompanyErasureMember{memberA}, sourceA.SourceID,
		firstHash, true, CompanyErasureImpact{})
	if err != nil {
		t.Fatal(err)
	}
	finalHash, _ := AdvanceCompanyErasurePreviewHash(erasure, firstHash, []CompanyErasureMember{memberB})
	erasure, err = erasure.AdvancePreview(erasure.Version, []CompanyErasureMember{memberB}, "", finalHash,
		false, CompanyErasureImpact{Sources: 2})
	if err != nil || erasure.Status != CompanyErasureAwaitingApproval || erasure.PreviewHash != finalHash {
		t.Fatalf("preview=%+v err=%v", erasure, err)
	}
	if _, err := erasure.Approve(erasure.Version, erasure.PreviewHash, erasure.RequestedBy, now); err == nil {
		t.Fatal("requester approved their own destructive request")
	}
	approved, err := erasure.Approve(erasure.Version, erasure.PreviewHash, "human:approver", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approved.Begin(approved.Version, now.Add(30*time.Minute)); err == nil {
		t.Fatal("erasure began before retention time")
	}
	started, err := approved.Begin(approved.Version, now.Add(time.Hour))
	if err != nil || started.Status != CompanyErasureErasing {
		t.Fatalf("started=%+v err=%v", started, err)
	}
}

func TestCompanyErasureApprovalRequiresDifferentHumanPrincipal(t *testing.T) {
	erasure := CompanyErasure{RequestedBy: "human:alice:first", Status: CompanyErasureAwaitingApproval,
		PreviewHash: "sha256:preview", Version: 4}
	now := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, approver := range []string{"human:alice:second", "agent:bob:1", "tool:bob:1", "bob"} {
		if _, err := erasure.Approve(4, erasure.PreviewHash, approver, now); err == nil {
			t.Fatalf("approval unexpectedly accepted %q", approver)
		}
	}
	approved, err := erasure.Approve(4, erasure.PreviewHash, "human:bob:1", now)
	if err != nil || approved.ApprovedBy != "human:bob:1" {
		t.Fatalf("distinct human approval=%+v err=%v", approved, err)
	}
}
