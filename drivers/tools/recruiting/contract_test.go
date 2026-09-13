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
	for _, word := range []string{TypeSystemStatus, TypeScopeControlGet, TypeCapacityStatus} {
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
		TypeSourceGet, TypeSourceList, TypeSourceUpdate, TypeRecipeInspect, TypeRecipePrepare, TypeSystemStatus, TypeCapacityStatus,
		TypeRecipePropose, TypeRecipeValidate, TypeRecipeApprove,
		TypeWorkGet, TypeWorkList, TypeRepairGet, TypeRepairList, TypeSystemReconcile,
		TypeWorkResolve, TypeWorkRetry, TypeRepairValidate, TypeRepairResolve, TypeRepairRecover,
		TypeWorkCancel,
		TypeDeepDiscoveryStart, TypeDeepDiscoveryGet, TypeDeepDiscoveryCheckpoint,
		TypeDeepDiscoveryGraph, TypeDeepDiscoveryGuide, TypeDeepDiscoveryWait, TypeDeepDiscoveryResume, TypeDeepDiscoveryComplete, TypeDeepDiscoveryCancel,
		TypeDeepDiscoveryBrowserObserve, TypeDeepDiscoveryBrowserGet,
		TypeOnboardingBegin, TypeOnboardingStatus, TypeOnboardingMaterialize,
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
