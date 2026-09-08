package recruiting

import (
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestOperationalWorkQueryParsesFiltersAndTimeBounds(t *testing.T) {
	query, err := operationalWorkQuery(workListPayload{
		Status: model.WorkWaitingHuman, Purpose: " repair ", Trigger: " manual ", WaitingReason: " recipe_invalid ",
		Target: Target{Type: " source ", ID: " source-a "}, InitiatorActorID: " human:operator:1 ",
		UpdatedFrom: "2026-09-08T00:00:00Z", UpdatedBefore: "2026-09-09T00:00:00Z", PageRequest: PageRequest{Limit: 25},
	})
	if err != nil || query.Purpose != "repair" || query.TargetID != "source-a" || query.WaitingReason != "recipe_invalid" ||
		query.UpdatedFrom == nil || query.UpdatedBefore == nil || query.Limit != 25 {
		t.Fatalf("operational query = %+v err=%v", query, err)
	}
	for _, payload := range []workListPayload{
		{WaitingReason: "recipe_invalid"},
		{Status: "unknown"},
		{Target: Target{Type: "source"}},
		{UpdatedFrom: "not-a-time"},
		{UpdatedFrom: "2026-09-09T00:00:00Z", UpdatedBefore: "2026-09-08T00:00:00Z"},
	} {
		if _, err := operationalWorkQuery(payload); err == nil {
			t.Fatalf("invalid operational query was accepted: %+v", payload)
		}
	}
}
