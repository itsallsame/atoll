package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingTwoOperatorsApproveFrozenCompanyErasureAcrossRestart(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	requesterAPI := newAPIClient(t, h.base)
	requesterRegistration := requesterAPI.register("erasure-requester", "erasure-requester@example.test", "requester-password")
	requesterHome := stringField(t, requesterRegistration, "home_channel_id")
	approverAPI := newAPIClient(t, h.base)
	approverRegistration := approverAPI.register("erasure-approver", "erasure-approver@example.test", "approver-password")
	approverHome := stringField(t, approverRegistration, "home_channel_id")
	time.Sleep(500 * time.Millisecond)

	_, rootWS := rootClient(t, h, map[string]int64{c0ChannelID: 0})
	registrar := findRegistrar(t, rootWS)
	shared := registrarRequest(t, rootWS, c0ChannelID, registrar, "system.channel.create", map[string]any{
		"name": "erasure-operations",
		"initial_seats": []any{
			map[string]any{"source_actor_id": rootActorID(t, rootWS, c0ChannelID), "kind": "human", "principal": "root"},
			map[string]any{"kind": "human", "principal": "erasure-requester"},
			map[string]any{"kind": "human", "principal": "erasure-approver"},
		},
	})
	sharedID := stringField(t, shared, "channel_id")
	_ = awaitDoor(t, rootWS, sharedID)
	const controlDecl = "e2e-recruiting-company-erasure"
	registrarRequest(t, rootWS, sharedID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Two-person Company erasure control.",
		"config": map[string]any{"executor_id": "unused-erasure-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false}, "visibility": "private",
	})
	intro := rootWS.request(sharedID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, rootWS, sharedID, controlID, h.server)

	requesterWS := dialWS(t, h.base, requesterAPI.cookieHeader(), map[string]int64{requesterHome: 0, sharedID: 0})
	company := requesterWS.request(sharedID, "recruiting.company.add", controlID, map[string]any{
		"command_id": "e2e-erasure-company-add", "company_id": "e2e-erasure-company",
		"name": "Erasure Company", "website": "https://erasure.e2e.example", "reason": "prepare compliance fixture",
	})
	const artifactRef = "artifact://e2e-company-erasure-object"
	requesterWS.resource(map[string]any{"channel_id": sharedID, "op": "create", "resource_id": artifactRef,
		"args": json.RawMessage(`{"sensitive":"historical recruiting evidence"}`)})
	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	historicalWork, _ := model.NewWork("e2e-erasure-historical-work", "company", "e2e-erasure-company",
		"diagnostic", "manual")
	if err := storeWorkForErasureE2E(db, historicalWork, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO recruiting_artifacts(artifact_id, artifact_kind, content_hash, object_ref,
work_id, attempt_id, access_scope, retention_policy, redacted, rejected, created_at)
VALUES (?, 'response', 'sha256:e2e-erasure-artifact', ?, ?, NULL, 'company', 'compliance', FALSE, FALSE, ?)`,
		"e2e-erasure-artifact", artifactRef, historicalWork.WorkID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"a", "b"} {
		source := requesterWS.request(sharedID, "recruiting.source.add", controlID, map[string]any{
			"command_id": "e2e-erasure-source-add-" + suffix, "source_id": "e2e-erasure-source-" + suffix,
			"company_id": "e2e-erasure-company", "endpoint": "https://erasure.e2e.example/" + suffix,
			"category": "all", "discovery_generation": 1, "reason": "prepare archived Source scope",
		})
		requesterWS.request(sharedID, "recruiting.source.archive", controlID, map[string]any{
			"command_id":       "e2e-erasure-source-archive-" + suffix,
			"target":           map[string]any{"target_type": "source", "target_id": "e2e-erasure-source-" + suffix},
			"expected_version": nestedNumberField(t, source, "source", "version"), "reason": "settle Source before erasure",
		})
	}
	archived := requesterWS.request(sharedID, "recruiting.company.archive", controlID, map[string]any{
		"command_id":       "e2e-erasure-company-archive",
		"target":           map[string]any{"target_type": "company", "target_id": "e2e-erasure-company"},
		"expected_version": nestedNumberField(t, company, "company", "version"), "reason": "archive before compliance erasure",
	})
	previewPayload := map[string]any{
		"command_id": "e2e-erasure-preview", "erasure_id": "e2e-erasure-request",
		"company_id": "e2e-erasure-company", "expected_version": nestedNumberField(t, archived, "company", "version"),
		"policy_version": "policy-2026-09", "execute_after": time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano),
		"reason": "verified data-subject compliance request",
	}
	started := requesterWS.request(sharedID, "recruiting.company.erasure.preview", controlID, previewPayload)
	if nestedStringField(t, started, "erasure", "status") != "previewing" ||
		!strings.HasPrefix(stringField(t, started, "requested_by"), "human:erasure-requester:") {
		t.Fatalf("erasure preview start=%v", started)
	}
	if _, terminal, err := requesterWS.tryRequest(sharedID, "recruiting.company.restore", controlID, map[string]any{
		"command_id":       "e2e-erasure-blocked-company-restore",
		"target":           map[string]any{"target_type": "company", "target_id": "e2e-erasure-company"},
		"expected_version": nestedNumberField(t, archived, "company", "version"),
		"reason":           "must not invalidate an active compliance scope",
	}); err == nil || terminal["error_code"] != "quality_rejected" {
		t.Fatalf("active erasure allowed Company restore terminal=%v err=%v", terminal, err)
	}
	first := requesterWS.request(sharedID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 1})
	if numberField(t, first, "company_erasure_previewed") != 1 || first["company_erasure_preview_done"] == true {
		t.Fatalf("first bounded erasure page=%v", first)
	}

	h.restartServer()
	requesterAPI = newAPIClient(t, h.base)
	if login := requesterAPI.login("erasure-requester@example.test", "requester-password"); login["id"] != "erasure-requester" {
		t.Fatalf("requester login after restart=%v", login)
	}
	requesterWS = dialWS(t, h.base, requesterAPI.cookieHeader(), map[string]int64{requesterHome: 0, sharedID: 0})
	waitRecruitingReady(t, requesterWS, sharedID, controlID, h.server)
	second := requesterWS.request(sharedID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 1})
	if numberField(t, second, "company_erasure_previewed") != 1 || second["company_erasure_preview_done"] != true {
		t.Fatalf("restart did not finish bounded erasure preview=%v", second)
	}
	ready := requesterWS.request(sharedID, "recruiting.company.erasure.get", controlID,
		map[string]any{"erasure_id": "e2e-erasure-request"})
	if nestedStringField(t, ready, "erasure", "status") != "awaiting_approval" ||
		nestedNumberField(t, ready, "erasure", "source_count") != 2 ||
		nestedStringField(t, ready, "work", "waiting_reason") != "compliance_approval_required" {
		t.Fatalf("frozen erasure preview=%v", ready)
	}
	approvePayload := map[string]any{
		"command_id": "e2e-erasure-approve", "erasure_id": "e2e-erasure-request",
		"expected_version": nestedNumberField(t, ready, "erasure", "version"),
		"preview_hash":     nestedStringField(t, ready, "erasure", "preview_hash"),
		"reason":           "independent operator verified the exact frozen impact",
	}
	if _, terminal, err := requesterWS.tryRequest(sharedID, "recruiting.company.erasure.approve", controlID,
		approvePayload); err == nil || terminal["error_code"] != "quality_rejected" {
		t.Fatalf("requester self-approval terminal=%v err=%v", terminal, err)
	}

	approverAPI = newAPIClient(t, h.base)
	if login := approverAPI.login("erasure-approver@example.test", "approver-password"); login["id"] != "erasure-approver" {
		t.Fatalf("approver login=%v", login)
	}
	approverWS := dialWS(t, h.base, approverAPI.cookieHeader(), map[string]int64{approverHome: 0, sharedID: 0})
	approved := approverWS.request(sharedID, "recruiting.company.erasure.approve", controlID, approvePayload)
	if nestedStringField(t, approved, "erasure", "status") != "approved" ||
		!strings.HasPrefix(nestedStringField(t, approved, "erasure", "approved_by"), "human:erasure-approver:") ||
		nestedStringField(t, approved, "work", "waiting_reason") != "compliance_retention_wait" {
		t.Fatalf("second-person erasure approval=%v", approved)
	}
	replayed := approverWS.request(sharedID, "recruiting.company.erasure.approve", controlID, approvePayload)
	if nestedNumberField(t, replayed, "erasure", "version") != nestedNumberField(t, approved, "erasure", "version") {
		t.Fatalf("erasure approval did not replay=%v", replayed)
	}
	execution := approverWS.request(sharedID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 1})
	if numberField(t, execution, "company_erasure_resources") != 1 ||
		execution["company_erasure_manifest_done"] != true ||
		stringField(t, execution, "company_erasure_purge_phase") == "" {
		t.Fatalf("erasure Resource manifest=%v", execution)
	}
	resources := approverWS.request(sharedID, "recruiting.company.erasure.resources", controlID,
		map[string]any{"erasure_id": "e2e-erasure-request", "limit": 1})
	items := sliceField(t, resources, "resources")
	if len(items) != 1 || stringField(t, items[0].(map[string]any), "object_ref") != artifactRef {
		t.Fatalf("erasure Resource page=%v", resources)
	}
	verifyPayload := map[string]any{"command_id": "e2e-erasure-resource-verify",
		"erasure_id": "e2e-erasure-request", "artifact_id": "e2e-erasure-artifact",
		"expected_version": numberField(t, items[0].(map[string]any), "version"),
		"reason":           "verified Resource tombstone after owner deletion"}
	if _, terminal, err := approverWS.tryRequest(sharedID, "recruiting.company.erasure.resource.verify_absent",
		controlID, verifyPayload); err == nil || terminal["error_code"] != "quality_rejected" {
		t.Fatalf("existing Resource was reported absent terminal=%v err=%v", terminal, err)
	}
	requesterWS.resource(map[string]any{"channel_id": sharedID, "op": "delete", "resource_id": artifactRef})
	verified := approverWS.request(sharedID, "recruiting.company.erasure.resource.verify_absent", controlID, verifyPayload)
	if nestedStringField(t, verified, "resource", "status") != "deleted" ||
		!strings.HasPrefix(nestedStringField(t, verified, "resource", "resolution_by"), "human:erasure-approver:") {
		t.Fatalf("Resource absence verification=%v", verified)
	}
	var completedView map[string]any
	for step := 0; step < 150; step++ {
		approverWS.request(sharedID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 1})
		view := approverWS.request(sharedID, "recruiting.company.erasure.get", controlID,
			map[string]any{"erasure_id": "e2e-erasure-request"})
		if nestedStringField(t, view, "erasure", "status") == "completed" {
			completedView = view
			break
		}
	}
	if completedView == nil || nestedStringField(t, completedView, "work", "resolution") != "succeeded" ||
		nestedStringField(t, completedView, "proof", "proof_hash") == "" ||
		nestedNumberField(t, completedView, "proof", "source_count") != 2 ||
		nestedNumberField(t, completedView, "proof", "resource_count") != 1 {
		t.Fatalf("completed Company erasure view=%v", completedView)
	}
	if _, terminal, err := approverWS.tryRequest(sharedID, "recruiting.company.get", controlID,
		map[string]any{"company_id": "e2e-erasure-company"}); err == nil || terminal["error_code"] != "not_found" {
		t.Fatalf("erased Company remained readable terminal=%v err=%v", terminal, err)
	}
	if _, terminal, err := approverWS.tryRequest(sharedID, "recruiting.company.add", controlID, map[string]any{
		"command_id": "e2e-erasure-company-id-reuse", "company_id": "e2e-erasure-company",
		"name": "Forbidden Reuse", "website": "https://reused.erasure.example",
		"reason": "prove erased identity is a permanent tombstone",
	}); err == nil || terminal["error_code"] != "business_key_conflict" {
		t.Fatalf("erased Company ID was reusable terminal=%v err=%v", terminal, err)
	}
	h.restartServer()
	approverAPI = newAPIClient(t, h.base)
	if login := approverAPI.login("erasure-approver@example.test", "approver-password"); login["id"] != "erasure-approver" {
		t.Fatalf("approver login after completed erasure=%v", login)
	}
	approverWS = dialWS(t, h.base, approverAPI.cookieHeader(), map[string]int64{approverHome: 0, sharedID: 0})
	waitRecruitingReady(t, approverWS, sharedID, controlID, h.server)
	recoveredProof := approverWS.request(sharedID, "recruiting.company.erasure.get", controlID,
		map[string]any{"erasure_id": "e2e-erasure-request"})
	if nestedStringField(t, recoveredProof, "proof", "proof_hash") !=
		nestedStringField(t, completedView, "proof", "proof_hash") {
		t.Fatalf("restart changed Company erasure proof=%v", recoveredProof)
	}
}

func storeWorkForErasureE2E(db *sql.DB, work model.Work, at time.Time) error {
	repository, err := store.NewRepository(db)
	if err != nil {
		return err
	}
	if err := repository.CreateWork(context.Background(), work,
		store.WorkPlacement{CompanyID: "e2e-erasure-company", NotBefore: at}, at); err != nil {
		return err
	}
	running, err := work.Start(work.Version)
	if err != nil {
		return err
	}
	if err := repository.UpdateWorkCAS(context.Background(), work.Version, running, at); err != nil {
		return err
	}
	completed, err := running.Complete(running.Version, model.ResolutionSucceeded, "", "")
	if err != nil {
		return err
	}
	return repository.UpdateWorkCAS(context.Background(), running.Version, completed, at)
}
