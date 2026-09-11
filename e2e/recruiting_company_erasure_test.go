package e2e

import (
	"strings"
	"testing"
	"time"
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
		"policy_version": "policy-2026-09", "execute_after": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
		"reason": "verified data-subject compliance request",
	}
	started := requesterWS.request(sharedID, "recruiting.company.erasure.preview", controlID, previewPayload)
	if nestedStringField(t, started, "erasure", "status") != "previewing" ||
		!strings.HasPrefix(stringField(t, started, "requested_by"), "human:erasure-requester:") {
		t.Fatalf("erasure preview start=%v", started)
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
}
