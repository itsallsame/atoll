package recruiting

import (
	"encoding/json"
	"testing"
)

func TestCommonContractIsVersionedAndCursorIsOpaque(t *testing.T) {
	if ContractVersion == "" {
		t.Fatal("contract version is empty")
	}
	if err := (PageRequest{Cursor: "opaque:/+=", Limit: 100}).Validate(100); err != nil {
		t.Fatalf("valid opaque cursor rejected: %v", err)
	}
	for _, p := range []PageRequest{{Limit: -1}, {Limit: 101}, {Cursor: "bad\nvalue"}} {
		if err := p.Validate(100); err == nil {
			t.Fatalf("invalid page request accepted: %+v", p)
		}
	}
}

func TestOperationalStatusWordsAreExposed(t *testing.T) {
	words := manifest().Words
	for _, word := range []string{TypeSystemStatus, TypeScopeControlGet, TypeCapacityStatus, TypeConsoleSnapshot} {
		if _, found := words[word]; !found {
			t.Fatalf("operational query %s is absent from the recruiting manifest", word)
		}
	}
}

func TestNaturalLanguageOnboardingWordsPublishInputSchemas(t *testing.T) {
	words := manifest().Words
	for _, word := range []string{
		TypeCompanyAdd, TypeCompanyGet, TypeCompanyList, TypeCompanyUpdate,
		TypeSourceDiscover, TypeSourceDiscoveryGet, TypeSourceDiscoveryCandidates,
		TypeSourceGet, TypeSourceList, TypeSourceUpdate, TypeSourceValidate, TypeSourceValidationPublish,
		TypeRecipeInspect, TypeRecipePrepare, TypeSystemStatus, TypeCapacityStatus, TypeConsoleSnapshot,
		TypeRecipePropose, TypeRecipeValidate, TypeRecipeApprove, TypeRecipeAssign, TypeBaselineStart,
		TypeWorkGet, TypeWorkList, TypeRepairGet, TypeRepairList, TypeSystemReconcile,
		TypeWorkResolve, TypeWorkRetry, TypeRepairValidate, TypeRepairResolve, TypeRepairRecover,
		TypeWorkCancel,
		TypeDeepDiscoveryStart, TypeDeepDiscoveryGet, TypeDeepDiscoveryCheckpoint,
		TypeDeepDiscoveryGraph, TypeDeepDiscoveryGuide, TypeDeepDiscoveryWait, TypeDeepDiscoveryResume, TypeDeepDiscoveryComplete, TypeDeepDiscoveryCancel,
		TypeDeepDiscoveryBrowserObserve, TypeDeepDiscoveryBrowserGet,
		TypeOnboardingBegin, TypeOnboardingStatus, TypeOnboardingAdvance, TypeOnboardingMaterialize,
	} {
		schema := words[word].InputSchema
		if len(schema) == 0 || !json.Valid(schema) {
			t.Fatalf("word %s has no valid input schema: %s", word, schema)
		}
		var decoded struct {
			Type                 string `json:"type"`
			AdditionalProperties bool   `json:"additionalProperties"`
		}
		if err := json.Unmarshal(schema, &decoded); err != nil || decoded.Type != "object" || decoded.AdditionalProperties {
			t.Fatalf("word %s schema is not a closed object: %s (err=%v)", word, schema, err)
		}
	}
}

func TestOnboardingAdvanceAcceptsOnlyCompanyName(t *testing.T) {
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(manifest().Words[TypeOnboardingAdvance].InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "company_name" || len(schema.Properties) != 1 {
		t.Fatalf("onboarding.advance exposed internal control fields: %+v", schema)
	}
}

func TestRecipePreparePublishesSemanticMappingInsteadOfRawABI(t *testing.T) {
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(manifest().Words[TypeRecipePrepare].InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if _, found := schema.Properties["mapping"]; !found {
		t.Fatal("recipe.prepare does not publish the semantic mapping contract")
	}
	if _, found := schema.Properties["spec"]; found {
		t.Fatal("recipe.prepare still asks an Agent to construct the low-level Recipe ABI")
	}
	foundMapping := false
	for _, field := range schema.Required {
		if field == "mapping" {
			foundMapping = true
		}
	}
	if !foundMapping {
		t.Fatal("recipe.prepare does not require the deterministic mapping path")
	}
}

func TestSourceValidationPublishDerivesFactsFromCompletedWork(t *testing.T) {
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(manifest().Words[TypeSourceValidationPublish].InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if _, found := schema.Properties["validation_work_id"]; !found {
		t.Fatal("source validation publish does not accept the completed Work hand-off")
	}
	for _, lowLevel := range []string{"recipe_id", "checkpoint_strategy", "overlap_pages", "evidence_artifact_ids"} {
		if _, found := schema.Properties[lowLevel]; found {
			t.Fatalf("source validation publish still asks the Agent for derived field %s", lowLevel)
		}
	}
}
