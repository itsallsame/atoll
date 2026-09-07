package society

import (
	"encoding/json"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/society/model"
)

func TestBrowserViewsFitFeedProjectionAndInspectCitizen(t *testing.T) {
	cfg, err := model.DefaultConfig("A")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := model.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	state := &runtimeState{engine: engine}

	population := currentPopulation(state)
	if len(population.Citizens) != 100 || len(population.Firms) != 10 {
		t.Fatalf("population view citizens=%d firms=%d", len(population.Citizens), len(population.Firms))
	}
	assertBelowFeedBudget(t, population)
	network := currentNetwork(state)
	if len(network.Edges) != 250 {
		t.Fatalf("network edges=%d want 250", len(network.Edges))
	}
	assertBelowFeedBudget(t, network)

	inspected, found := inspectCitizen(state, "citizen-001")
	if !found || inspected.Citizen.ID != "citizen-001" || inspected.Household == nil || len(inspected.Relationships) == 0 {
		t.Fatalf("incomplete citizen inspection: found=%v value=%+v", found, inspected)
	}
	assertBelowFeedBudget(t, inspected)
}

func assertBelowFeedBudget(t *testing.T, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 10<<10 {
		t.Fatalf("browser view is %d bytes; must remain below 10 KiB feed projection", len(raw))
	}
}
