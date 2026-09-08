package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunIsDeterministicAndMeetsEndCondition(t *testing.T) {
	b := testBundle(t)
	first, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	second, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same bundle produced different histories")
	}
	if !first.Summary.EndConditionMet || first.Summary.RegistrationState != "attached_pending" {
		t.Fatalf("unexpected ending: %+v", first.Summary)
	}
	if first.Summary.EmergentEvents < 3 {
		t.Fatalf("only %d emergent events", first.Summary.EmergentEvents)
	}
}

func TestEveryEventHasPastCausesAndKnownObservers(t *testing.T) {
	b := testBundle(t)
	result, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLedger(result.Events, b.Characters); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateDocumentComparisonDoesNotLeakToAbsentCharacters(t *testing.T) {
	b := testBundle(t)
	result, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range result.Events {
		if e.SourceID != "decision_fragment" {
			continue
		}
		for _, absent := range []string{"he_asheng", "lu_jinghe", "zhou_jiren", "shen_boan"} {
			if contains(e.Observers, absent) {
				t.Fatalf("private comparison leaked to %s", absent)
			}
		}
		return
	}
	t.Fatal("missing decision_fragment")
}

func TestChangingCharacterUtilitiesChangesTheHistory(t *testing.T) {
	b := testBundle(t)
	for i := range b.Scenario.DecisionPoints {
		decision := &b.Scenario.DecisionPoints[i]
		if decision.ID != "decision_fragment" {
			continue
		}
		for j := range decision.Options {
			if decision.Options[j].ID == "disclose_immediately" {
				decision.Options[j].Utility["new_pressure"] = 20
			}
		}
	}
	result, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range result.Events {
		if e.SourceID == "decision_fragment" {
			if e.SelectedOption != "disclose_immediately" {
				t.Fatalf("selected %q after utility change", e.SelectedOption)
			}
			return
		}
	}
	t.Fatal("missing decision_fragment")
}

func TestTimelineInterleavesSeedsAndDecisionsByOrder(t *testing.T) {
	result, err := run(testBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"seed_lin_claim", "seed_he_claim", "seed_rain_rumor", "decision_fragment", "decision_lu_evidence", "seed_zhou_arrives", "decision_zhou_process", "decision_boundary", "decision_he_testimony", "decision_registration"}
	var got []string
	for _, e := range result.Events {
		got = append(got, e.SourceID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("timeline order = %v, want %v", got, want)
	}
}

func TestMemoriesOnlyReferenceObservedEvents(t *testing.T) {
	b := testBundle(t)
	result, err := run(b)
	if err != nil {
		t.Fatal(err)
	}
	events := make(map[string]event)
	for _, e := range result.Events {
		events[e.EventID] = e
	}
	for _, m := range result.Memories {
		for _, id := range m.ObservedEventIDs {
			if !contains(events[id].Observers, m.Character) {
				t.Fatalf("%s remembers unobserved event %s", m.Character, id)
			}
		}
	}
}

func TestResultRemainsJSONSerializable(t *testing.T) {
	result, err := run(testBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Scene, "分支记录") {
		t.Fatal("scene material lacks branch record")
	}
	if !strings.Contains(result.Chapter, "第一张挂号") {
		t.Fatal("run lacks composed chapter")
	}
}

func TestRunAllCarriesDayOneStateIntoDayTwo(t *testing.T) {
	out := t.TempDir()
	if err := runAll("../../narrative", out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "day-02", "state-diff.json"))
	if err != nil {
		t.Fatal(err)
	}
	var diff stateDiff
	if err := json.Unmarshal(data, &diff); err != nil {
		t.Fatal(err)
	}
	if got := diff.Before["registration.south_field_7"]; got != "attached_pending" {
		t.Fatalf("day two inherited registration=%v", got)
	}
	if got := diff.After["claimant.lin_suyun.recorded_as"]; got != "林素云" {
		t.Fatalf("day two claimant name=%v", got)
	}
	if _, err := os.Stat(filepath.Join(out, "index.json")); err != nil {
		t.Fatal(err)
	}
}

func testBundle(t *testing.T) bundle {
	t.Helper()
	b, err := loadBundle("../../narrative", "../../narrative/scenarios/day-01.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
