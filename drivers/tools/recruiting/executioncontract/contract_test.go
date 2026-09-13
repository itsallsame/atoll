package executioncontract

import (
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestStableToolTargetMatchesOnlyItsAuthenticatedSeat(t *testing.T) {
	for _, target := range []string{"tool:executor", "tool:executor:123"} {
		if !ValidToolTarget(target) {
			t.Fatalf("valid tool target rejected: %q", target)
		}
	}
	for _, target := range []string{"", "executor", "agent:executor", "tool:executor:123:extra", "tool:executor name"} {
		if ValidToolTarget(target) {
			t.Fatalf("invalid tool target accepted: %q", target)
		}
	}
	if !TargetMatchesAuthenticatedActor("tool:executor", "tool:executor:123") ||
		!TargetMatchesAuthenticatedActor("tool:executor:123", "tool:executor:123") {
		t.Fatal("configured executor did not match its authenticated seat")
	}
	if TargetMatchesAuthenticatedActor("tool:executor", "tool:other:123") ||
		TargetMatchesAuthenticatedActor("tool:executor:123", "tool:executor:456") ||
		TargetMatchesAuthenticatedActor("tool:executor", "tool:executor") {
		t.Fatal("configured executor matched a wrong or non-concrete sender")
	}
}

func TestFailureReportBindsClassificationAndArtifactToAttempt(t *testing.T) {
	artifact, err := model.NewArtifactMetadata("failure-1", model.ArtifactFailure, "sha256:failure", "artifact://failure-1",
		"work-1", "attempt-1", "operators", "30d", true)
	if err != nil {
		t.Fatal(err)
	}
	report := FailureReport{Class: "parse_error", Signature: "listing.parse_error", NeedsRepair: true, Artifact: artifact}
	if err := report.Validate("attempt-1"); err != nil {
		t.Fatal(err)
	}
	if report.StableSignature() != "listing.parse_error" {
		t.Fatalf("stable signature = %q", report.StableSignature())
	}
	legacy := report
	legacy.Signature = ""
	if legacy.StableSignature() != legacy.Class || legacy.Validate("attempt-1") != nil {
		t.Fatal("legacy failure did not fall back to its bounded class")
	}
	invalidSignature := report
	invalidSignature.Signature = "raw URL https://secret.example/path"
	if err := invalidSignature.Validate("attempt-1"); err == nil {
		t.Fatal("raw failure detail was accepted as a single-flight signature")
	}
	report.NeedsRepair = false
	if err := report.Validate("attempt-1"); err == nil {
		t.Fatal("parse failure was allowed to suppress repair classification")
	}
	report.NeedsRepair = true
	if err := report.Validate("attempt-2"); err == nil {
		t.Fatal("failure Artifact was accepted for another Attempt")
	}
	report.Artifacts = []model.ArtifactMetadata{artifact, artifact}
	if err := report.Validate("attempt-1"); err == nil {
		t.Fatal("duplicate supporting failure Artifact was accepted")
	}
}

func TestDeepDiscoveryEffectAttestationRequiresConsistentReadOnlyEvidence(t *testing.T) {
	safe := DeepDiscoveryEffectAttestation{DocumentNavigations: 1, ObservedMethods: []string{"GET", "OPTIONS"},
		PublicEndpoint: true, RobotsAllowed: true, TermsPolicyVersion: 1}
	if err := safe.Validate(1); err != nil {
		t.Fatal(err)
	}
	blocked := safe
	blocked.BlockedMethods, blocked.BlockedWriteRequests = []string{"POST"}, 1
	if err := blocked.Validate(1); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*DeepDiscoveryEffectAttestation){
		"missing observed method": func(value *DeepDiscoveryEffectAttestation) { value.ObservedMethods = nil },
		"allowed write":           func(value *DeepDiscoveryEffectAttestation) { value.AllowedWriteRequests = 1 },
		"safe method blocked": func(value *DeepDiscoveryEffectAttestation) {
			value.BlockedMethods = []string{"GET"}
			value.BlockedWriteRequests = 1
		},
		"missing blocked method": func(value *DeepDiscoveryEffectAttestation) { value.BlockedWriteRequests = 1 },
	} {
		value := safe
		mutate(&value)
		if err := value.Validate(1); err == nil {
			t.Fatalf("%s attestation was accepted: %+v", name, value)
		}
	}
}
