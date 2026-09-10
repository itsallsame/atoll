package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingOperatorStartsDeviceBoundProfileRepairThroughServer(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("profile-repair-operator", "profile-repair-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlDecl = "e2e-profile-repair-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlDecl, "name": controlDecl, "class": "recruiting",
		"description": "Recruiting Profile repair control.",
		"config": map[string]any{"executor_id": "unused-profile-repair-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false, "profile_repair_session_ttl_ms": 600000},
		"visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlDecl})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	profile, incident := seedProfileRepairForPublicE2E(t, runtimeDSN, time.Now().UTC().Truncate(time.Second))
	payload := map[string]any{
		"command_id": "e2e-profile-repair-begin", "target": map[string]any{
			"target_type": "profile", "target_id": profile.ProfileID,
		},
		"expected_version": profile.Version, "repair_incident_id": incident.IncidentID,
		"reason": "operator starts a bounded repair on the authorized browser device",
	}
	started := ws.request(homeID, "recruiting.profile.repair.begin", controlID, payload)
	encoded, _ := json.Marshal(started)
	if nestedStringField(t, started, "profile", "auth_status") != "repairing" ||
		nestedStringField(t, started, "session", "status") != "awaiting_device" ||
		stringField(t, started, "next_action") != "complete_on_authorized_device" ||
		strings.Contains(string(encoded), profile.SecretRef) || strings.Contains(string(encoded), profile.DeviceID) {
		t.Fatalf("unsafe or incomplete Profile repair response=%s", encoded)
	}
	replayed := ws.request(homeID, "recruiting.profile.repair.begin", controlID, payload)
	replayedJSON, _ := json.Marshal(replayed)
	if string(replayedJSON) != string(encoded) {
		t.Fatalf("Profile repair command replay changed response: first=%s replay=%s", encoded, replayedJSON)
	}
	if _, terminal, err := ws.tryRequest(homeID, "recruiting.execution.offer", controlID, map[string]any{
		"command_id": "human-must-not-claim-profile-repair", "executor_incarnation": "browser-1",
		"capability": store.ProfileRepairCapability, "profile_id": profile.ProfileID,
	}); err == nil || terminal["error_code"] == nil {
		t.Fatalf("ordinary human claimed executor authority: terminal=%v err=%v", terminal, err)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("profile-repair-operator@example.test", "operator-local-password"); login["id"] != "profile-repair-operator" {
		t.Fatalf("Profile repair operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	inspected := recovered.request(homeID, "recruiting.profile.get", controlID, map[string]any{"profile_id": profile.ProfileID})
	inspectedJSON, _ := json.Marshal(inspected)
	if nestedStringField(t, inspected, "profile", "auth_status") != "repairing" ||
		nestedStringField(t, inspected, "latest_repair_session", "status") != "awaiting_device" ||
		strings.Contains(string(inspectedJSON), profile.SecretRef) || strings.Contains(string(inspectedJSON), profile.DeviceID) {
		t.Fatalf("restart lost or leaked Profile repair projection=%s", inspectedJSON)
	}
}

func seedProfileRepairForPublicE2E(t *testing.T, dsn string,
	now time.Time) (model.BrowserProfile, model.RepairIncident) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	profile, _ := model.NewBrowserProfileWithVerification("e2e-profile-repair-profile", "jobs.example.test",
		"tool:e2e-authorized-browser", "secret://e2e/profile/v1", model.ProfileVerificationRecipe{
			EndpointURL: "https://jobs.example.test/private/canary", RecipeID: "e2e-profile-canary", RecipeVersion: 1,
			ContentHash: "sha256:" + strings.Repeat("a", 64), ContractHash: "sha256:" + strings.Repeat("b", 64),
			Kind: model.RecipeDetail, Execution: model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
				ContentRef: "recipe://e2e/profile-canary", RequiredCapability: store.ProfileRepairCapability,
				Transport: model.RecipeTransportBrowser}, MinimumRecordCount: 1})
	if err := repository.CreateProfile(ctx, profile, now); err != nil {
		t.Fatal(err)
	}
	repairing, _ := profile.BeginRepair(profile.Version)
	if err := repository.UpdateProfileCAS(ctx, profile.Version, repairing, now); err != nil {
		t.Fatal(err)
	}
	affected, _ := model.NewWork("e2e-profile-repair-affected", "source", "source-e2e-profile", "listing_sync", "timer")
	if err := repository.CreateWork(ctx, affected, store.WorkPlacement{BusinessKey: affected.WorkID,
		Capability: "browser.recipe", ProfileID: profile.ProfileID, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	repairWork, _ := model.NewWork("e2e-profile-repair-incident-work", "repair_incident",
		"e2e-profile-repair-incident", "repair", "automatic")
	if err := repository.CreateWork(ctx, repairWork, store.WorkPlacement{BusinessKey: repairWork.WorkID,
		Priority: 500, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	incident, _ := model.NewRepairIncident("e2e-profile-repair-incident", model.FailureProfile,
		profile.ProfileID, "browser.profile.expired", "profile:1", affected.WorkID)
	incident, _ = incident.WithRepairWork(repairWork.WorkID)
	if _, joined, err := repository.OpenOrJoinRepair(ctx, incident, now); err != nil || joined {
		t.Fatalf("seed Profile repair incident joined=%v err=%v", joined, err)
	}
	return repairing, incident
}
