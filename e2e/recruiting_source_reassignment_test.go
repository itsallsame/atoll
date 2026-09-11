package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingOperatorReassignsSourceAcrossCompanyWithoutRewritingHistory(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	const oldSourceID = "e2e-reassign-old-source"
	seedReadyRecruitingSource(t, runtimeDSN, oldSourceID, time.Now().UTC().Add(-time.Hour))
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("source-reassign-operator", "source-reassign@example.test", "source-reassign-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})

	const controlDecl = "e2e-recruiting-source-reassignment"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Source reassignment control.",
		"config": map[string]any{"executor_id": "unused-reassignment-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false}, "visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	target := ws.request(homeID, "recruiting.company.add", controlID, map[string]any{
		"command_id": "e2e-reassign-target-add", "company_id": "e2e-reassign-target-company",
		"name": "Reassignment Target", "website": "https://target.reassign.example",
		"reason": "prepare reviewed ownership correction",
	})
	previewPayload := map[string]any{
		"command_id": "e2e-source-reassign-preview", "preview_id": "e2e-source-reassign-preview-1",
		"relation": "supersedes", "source_id": oldSourceID, "expected_source_version": 3,
		"target_company_id":        "e2e-reassign-target-company",
		"expected_company_version": nestedNumberField(t, target, "company", "version"),
		"new_source_id":            "e2e-reassign-new-source", "discovery_generation": 1,
		"reason": "operator verified the list is owned by the target company",
	}
	preview := ws.request(homeID, "recruiting.source.reassign.preview", controlID, previewPayload)
	if nestedStringField(t, preview, "preview", "status") != "ready" ||
		nestedStringField(t, preview, "preview", "preview_hash") == "" ||
		nestedNumberField(t, preview, "preview", "checkpoint_version") != 1 ||
		nestedNumberField(t, preview, "preview", "listing_assignment_version") != 1 {
		t.Fatalf("Source reassignment preview=%v", preview)
	}
	confirmPayload := map[string]any{
		"command_id": "e2e-source-reassign-confirm", "preview_id": "e2e-source-reassign-preview-1",
		"expected_version": 1, "preview_hash": nestedStringField(t, preview, "preview", "preview_hash"),
		"reason": "confirm the exact reviewed ownership cutover",
	}
	confirmed := ws.request(homeID, "recruiting.source.reassign.confirm", controlID, confirmPayload)
	if nestedStringField(t, confirmed, "preview", "status") != "confirmed" ||
		nestedStringField(t, confirmed, "previous_source", "control_status") != "archived" ||
		nestedStringField(t, confirmed, "previous_source", "company_id") != "company-"+oldSourceID ||
		nestedStringField(t, confirmed, "new_source", "company_id") != "e2e-reassign-target-company" ||
		nestedStringField(t, confirmed, "new_source", "readiness_status") != "candidate" ||
		nestedStringField(t, confirmed, "lineage", "relation") != "supersedes" ||
		stringField(t, confirmed, "next_action") != "validate_new_source_before_daily_eligibility" {
		t.Fatalf("Source reassignment confirmation=%v", confirmed)
	}
	operationID := stringField(t, confirmed, "scope_control_operation_id")
	if operationID == "" {
		t.Fatal("superseding reassignment omitted cancel operation")
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("source-reassign@example.test", "source-reassign-password"); login["id"] != "source-reassign-operator" {
		t.Fatalf("Source reassignment operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	replayed := recovered.request(homeID, "recruiting.source.reassign.confirm", controlID, confirmPayload)
	if nestedStringField(t, replayed, "preview", "status") != "confirmed" ||
		stringField(t, replayed, "scope_control_operation_id") != operationID {
		t.Fatalf("Source reassignment replay after restart=%v", replayed)
	}
	oldView := recovered.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": oldSourceID})
	newView := recovered.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": "e2e-reassign-new-source"})
	if nestedStringField(t, oldView, "entity", "company_id") != "company-"+oldSourceID ||
		nestedStringField(t, oldView, "entity", "control_status") != "archived" ||
		nestedStringField(t, newView, "entity", "company_id") != "e2e-reassign-target-company" ||
		nestedStringField(t, newView, "entity", "readiness_status") != "candidate" {
		t.Fatalf("recovered Source views old=%v new=%v", oldView, newView)
	}

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var oldCheckpoints, newCheckpoints, oldAssignments, newAssignments, lineages, operations, events int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_checkpoints WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_checkpoints WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_assignments WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_assignments WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_lineage WHERE from_source_id = ? AND to_source_id = ?),
  (SELECT COUNT(*) FROM recruiting_scope_control_operations WHERE operation_id = ? AND pause_mode = 'cancel'),
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_kind IN ('source.reassignment.previewed','source.reassignment.confirmed','source.created_from_lineage','source.superseded'))`,
		oldSourceID, "e2e-reassign-new-source", oldSourceID, "e2e-reassign-new-source", oldSourceID,
		"e2e-reassign-new-source", operationID).Scan(&oldCheckpoints, &newCheckpoints, &oldAssignments,
		&newAssignments, &lineages, &operations, &events); err != nil {
		t.Fatal(err)
	}
	if oldCheckpoints != 1 || newCheckpoints != 0 || oldAssignments != 1 || newAssignments != 0 ||
		lineages != 1 || operations != 1 || events != 4 {
		t.Fatalf("reassignment facts checkpoint=%d/%d assignment=%d/%d lineage=%d operation=%d events=%d",
			oldCheckpoints, newCheckpoints, oldAssignments, newAssignments, lineages, operations, events)
	}
}
