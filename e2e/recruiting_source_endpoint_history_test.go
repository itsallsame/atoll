package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingEndpointRedirectActivatesOnlyAfterValidatedCutover(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	const sourceID = "e2e-endpoint-history-source"
	seedReadyRecruitingSource(t, runtimeDSN, sourceID, time.Now().UTC().Add(-time.Hour))
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("endpoint-history-operator", "endpoint-history@example.test", "endpoint-history-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})

	const controlDecl = "e2e-recruiting-endpoint-history"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Endpoint redirect history control.",
		"config": map[string]any{"executor_id": "unused-endpoint-history-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false}, "visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	stagePayload := map[string]any{
		"command_id": "endpoint-history-stage", "target": map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": 3, "endpoint": "https://e2e-daily.example.test/careers",
		"transition_kind": "redirect", "reason": "site permanently redirects the jobs entry to careers",
	}
	staged := ws.request(homeID, "recruiting.source.update", controlID, stagePayload)
	stagedChange := staged["endpoint_change"].(map[string]any)
	fromEndpoint := stagedChange["from_endpoint"].(map[string]any)
	toEndpoint := stagedChange["to_endpoint"].(map[string]any)
	if nestedStringField(t, staged, "source", "readiness_status") != "repairing" ||
		stringField(t, stagedChange, "kind") != "redirect" || numberField(t, fromEndpoint, "revision") != 1 ||
		numberField(t, toEndpoint, "revision") != 2 {
		t.Fatalf("staged endpoint redirect=%v", staged)
	}
	changeID := stringField(t, stagedChange, "change_id")
	history := ws.request(homeID, "recruiting.source.endpoint.history", controlID,
		map[string]any{"source_id": sourceID, "limit": 10})
	items := sliceField(t, history, "items")
	if len(items) != 1 {
		t.Fatalf("staged endpoint history=%v", history)
	}
	first := items[0].(map[string]any)
	if _, activated := first["activation"]; activated {
		t.Fatalf("unvalidated endpoint appeared activated: %v", first)
	}

	validation := ws.request(homeID, "recruiting.source.validate", controlID, map[string]any{
		"command_id": "endpoint-history-validation", "run_id": "endpoint-history-validation-run",
		"work_id": "endpoint-history-validation-work", "recipe_id": "recipe-" + sourceID, "recipe_version": 1,
		"expected_assignment_version": 1,
		"target":                      map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version":            4, "reason": "validate redirected endpoint before cutover",
	})
	if nestedStringField(t, validation, "source", "readiness_status") != "validating" {
		t.Fatalf("endpoint redirect validation=%v", validation)
	}
	evidenceIDs := completeRestoredSourceValidationEvidence(t, runtimeDSN, sourceID,
		"endpoint-history-validation-work", "https://e2e-daily.example.test")
	published := ws.request(homeID, "recruiting.source.validation.publish", controlID, map[string]any{
		"command_id": "endpoint-history-publish", "recipe_id": "recipe-" + sourceID, "recipe_version": 1,
		"expected_assignment_version": 1, "identity": "verified", "pagination": "verified",
		"ordering": "verified", "update_retop": "verified", "checkpoint_strategy": "activity_desc",
		"overlap_pages": 1, "evidence_artifact_ids": evidenceIDs,
		"target":           map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": 5, "reason": "publish evidence-gated endpoint cutover",
	})
	publishedSource := published["source"].(map[string]any)
	publishedEndpoint := publishedSource["active_endpoint"].(map[string]any)
	if stringField(t, publishedEndpoint, "url") != "https://e2e-daily.example.test/careers" ||
		numberField(t, publishedEndpoint, "revision") != 2 {
		t.Fatalf("published endpoint redirect=%v", published)
	}
	history = ws.request(homeID, "recruiting.source.endpoint.history", controlID,
		map[string]any{"source_id": sourceID, "limit": 10})
	items = sliceField(t, history, "items")
	activation := items[0].(map[string]any)["activation"].(map[string]any)
	if stringField(t, activation, "change_id") != changeID ||
		numberField(t, activation, "activated_source_version") != 6 ||
		len(sliceField(t, activation, "evidence_artifact_ids")) == 0 {
		t.Fatalf("endpoint activation did not bind change and evidence: %v", activation)
	}

	rejectedStage := ws.request(homeID, "recruiting.source.update", controlID, map[string]any{
		"command_id": "endpoint-history-rejected-stage",
		"target":     map[string]any{"target_type": "source", "target_id": sourceID}, "expected_version": 6,
		"endpoint": "https://e2e-daily.example.test/unverified", "transition_kind": "correction",
		"reason": "investigate an operator-reported alternate endpoint",
	})
	if nestedStringField(t, rejectedStage, "endpoint_change", "kind") != "correction" {
		t.Fatalf("endpoint correction stage=%v", rejectedStage)
	}
	ws.request(homeID, "recruiting.source.validation.reject", controlID, map[string]any{
		"command_id": "endpoint-history-reject", "target": map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version": 7, "reason": "alternate endpoint failed operator review",
	})
	history = ws.request(homeID, "recruiting.source.endpoint.history", controlID,
		map[string]any{"source_id": sourceID, "limit": 10})
	items = sliceField(t, history, "items")
	if len(items) != 2 {
		t.Fatalf("endpoint history after rejection=%v", history)
	}
	if _, activated := items[0].(map[string]any)["activation"]; activated {
		t.Fatalf("rejected endpoint appeared activated: %v", items[0])
	}
	if _, activated := items[1].(map[string]any)["activation"]; !activated {
		t.Fatalf("validated redirect activation disappeared: %v", items[1])
	}
	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	repository, _ := store.NewRepository(db)
	checkpoint, err := repository.GetCheckpoint(ctx, sourceID)
	if err != nil || checkpoint.Version != 1 || checkpoint.LastOccurrenceID != "baseline-"+sourceID {
		t.Fatalf("endpoint cutover rewrote incremental checkpoint: %+v err=%v", checkpoint, err)
	}
}
