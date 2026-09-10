package narrative

import (
	"path/filepath"
	"testing"

	"github.com/wanpengxie/atoll/platform/channelspec"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/registry"
)

func testBundle(t *testing.T) string {
	t.Helper()
	bundle, err := filepath.Abs(filepath.Join("..", "..", "..", "narrative"))
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestNativeClassesAreServerPlacedAtollActors(t *testing.T) {
	if kind, ok := registry.ClassKind(WorldClass); !ok || kind != actor.KindTool {
		t.Fatalf("world class kind=%q ok=%v", kind, ok)
	}
	if placement, ok := registry.ClassPlacement(WorldClass); !ok || placement != channelspec.PlacementServer {
		t.Fatalf("world placement=%q ok=%v", placement, ok)
	}
	if kind, ok := registry.ClassKind(CharacterClass); !ok || kind != actor.KindAgent {
		t.Fatalf("character class kind=%q ok=%v", kind, ok)
	}
	for _, class := range []string{EnvironmentClass, ProjectorClass, PlannerClass} {
		if kind, ok := registry.ClassKind(class); !ok || kind != actor.KindTool {
			t.Fatalf("system class %s kind=%q ok=%v", class, kind, ok)
		}
		if placement, ok := registry.ClassPlacement(class); !ok || placement != channelspec.PlacementServer {
			t.Fatalf("system class %s placement=%q ok=%v", class, placement, ok)
		}
	}
	for _, class := range []string{WriterClass, CriticClass} {
		if kind, ok := registry.ClassKind(class); !ok || kind != actor.KindAgent {
			t.Fatalf("pipeline class %s kind=%q ok=%v", class, kind, ok)
		}
		if placement, ok := registry.ClassPlacement(class); !ok || placement != channelspec.PlacementServer {
			t.Fatalf("pipeline class %s placement=%q ok=%v", class, placement, ok)
		}
	}
}

func TestInitialStateImportsOfflineLedgerWithoutRepeatingRules(t *testing.T) {
	bundle := testBundle(t)
	world, err := loadInitialWorld(bundle, 5)
	if err != nil {
		t.Fatal(err)
	}
	if world.Day != 5 {
		t.Fatalf("initial day=%d want 5", world.Day)
	}
	if world.FiredRules["shen_compare_deed_to_stump"] == 0 {
		t.Fatal("world did not import previously fired rule")
	}
	private, err := loadInitialBelief(bundle, "shen_yanqiu", 5)
	if err != nil {
		t.Fatal(err)
	}
	if private.UsedRules["shen_compare_deed_to_stump"] == 0 {
		t.Fatal("character did not import its previously used rule")
	}
}

func TestCharacterProposalReadsPrivateBeliefsAndIsOneShot(t *testing.T) {
	spec, err := loadSimulation(testBundle(t))
	if err != nil {
		t.Fatal(err)
	}
	facts := map[string]any{
		"evidence.upper_deed": "submitted_sealed",
		"boundary.marker":     "exposed_maple_stump",
	}
	got := proposalsFor("shen_yanqiu", 6, "noon", spec.Policies, facts, map[string]int{}, map[string]int{})
	if len(got) != 1 || got[0].RuleID != "shen_compare_deed_to_stump" {
		t.Fatalf("proposals=%+v", got)
	}
	if got[0].Action == "" || got[0].ExpectedEffect == "" || len(got[0].Preconditions) == 0 {
		t.Fatalf("proposal has incomplete action intent: %+v", got[0])
	}
	used := map[string]int{"shen_compare_deed_to_stump": 6}
	if repeated := proposalsFor("shen_yanqiu", 6, "noon", spec.Policies, facts, used, map[string]int{"shen_compare_deed_to_stump": 1}); len(repeated) != 0 {
		t.Fatalf("one-shot rule repeated: %+v", repeated)
	}
}

func TestRecurringRuleHonorsCooldownAndLimit(t *testing.T) {
	candidate := rule{Mode: "recurring", CooldownDays: 2, MaxFirings: 3}
	if canFire(candidate, 5, 1, 6) {
		t.Fatal("rule fired inside cooldown")
	}
	if !canFire(candidate, 5, 1, 7) {
		t.Fatal("rule did not fire after cooldown")
	}
	if canFire(candidate, 5, 3, 7) {
		t.Fatal("rule exceeded max_firings")
	}
}

func TestCausalSourcesFollowReadStateRatherThanEventOrder(t *testing.T) {
	sources := map[string]string{"river.level": "E-water", "boundary.review": "E-review", "unrelated": "E-last"}
	got := causalSources(sources, []condition{{Key: "boundary.review"}, {Key: "river.level"}}, nil)
	want := []string{"E-review", "E-water"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("causal sources=%v want %v", got, want)
	}
}

func TestUtilityModifiersMakeStateAndRelationshipsAffectChoice(t *testing.T) {
	options := []option{
		{ID: "cooperate", Utility: map[string]int{"base": 3}, Modifiers: []modifier{{When: []condition{{Key: "trust.other", Op: "gte", Value: 2}}, Add: 5}}},
		{ID: "refuse", Utility: map[string]int{"base": 5}},
	}
	selected, err := choose(options, map[string]any{"trust.other": 3})
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "cooperate" {
		t.Fatalf("selected=%s want cooperate", selected.ID)
	}
}

func TestConditionsAndEffectsSupportDynamicQuantities(t *testing.T) {
	state := map[string]any{"resources.food": 4.0, "obsolete": true}
	if !conditionsMatch(state, []condition{{Key: "resources.food", Op: "lt", Value: 5}, {Key: "missing", Op: "not_exists"}}) {
		t.Fatal("extended conditions did not match")
	}
	delta := applyEffects(state, []effect{{Key: "resources.food", Op: "multiply", Value: 0.5}, {Key: "obsolete", Op: "delete"}})
	if state["resources.food"] != 2.0 || delta["resources.food"] != 2.0 {
		t.Fatalf("state=%v delta=%v", state, delta)
	}
	if _, exists := state["obsolete"]; exists {
		t.Fatal("delete effect did not remove key")
	}
}

func TestPeerTargetAddressesCharacterPrivateChannel(t *testing.T) {
	if got, want := string(peerTarget("narrative", "shen_yanqiu")), "peer:c0.narrative-shen-yanqiu"; got != want {
		t.Fatalf("peer target=%q want %q", got, want)
	}
}
