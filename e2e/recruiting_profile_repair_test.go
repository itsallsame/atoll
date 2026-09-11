package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
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

	canary := recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail,
		RequiredCapability: store.ProfileRepairCapability, Transport: recipeabi.TransportBrowser,
		Request: recipeabi.ReadRequest{Method: "GET", TimeoutMS: 10_000, MaxResponseBytes: 1 << 20,
			MaxRedirects: 2, UserAgent: "Atoll-Profile-Canary/1"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{"account": "#account"}}}
	canaryBytes, _ := json.Marshal(canary)
	canaryHash, _ := canary.ContentHash()
	const canaryRef = "recipe://e2e/profile-canary"
	ws.resource(map[string]any{"channel_id": homeID, "op": "create", "resource_id": canaryRef,
		"args": json.RawMessage(canaryBytes)})
	registeredProfile := ws.request(homeID, "recruiting.profile.register", controlID, map[string]any{
		"command_id": "e2e-profile-register", "profile_id": "e2e-profile-repair-profile",
		"security_domain": "jobs.example.test", "device_actor_id": "tool:e2e-authorized-browser",
		"canary_url": "https://jobs.example.test/private/canary", "canary_recipe_id": "e2e-profile-canary",
		"canary_recipe_version": 1, "canary_content_ref": canaryRef, "expected_content_hash": canaryHash,
		"canary_minimum_records": 1, "reason": "register an empty local browser slot for authenticated provisioning",
	})
	const profileID = "e2e-profile-repair-profile"
	const secretRef = "secret://local-browser-profile/e2e-profile-repair-profile/v1"
	incidentID := nestedStringField(t, registeredProfile, "provisioning", "repair_incident_id")
	if nestedStringField(t, registeredProfile, "profile", "auth_status") != "repairing" ||
		nestedNumberField(t, registeredProfile, "profile", "version") != 2 || incidentID == "" ||
		strings.Contains(mustJSON(registeredProfile), secretRef) || strings.Contains(mustJSON(registeredProfile), "tool:e2e-authorized-browser") {
		t.Fatalf("unsafe or incomplete Profile registration=%v", registeredProfile)
	}
	payload := map[string]any{
		"command_id": "e2e-profile-repair-begin", "target": map[string]any{
			"target_type": "profile", "target_id": profileID,
		},
		"expected_version": 2, "repair_incident_id": incidentID,
		"reason": "operator starts a bounded repair on the authorized browser device",
	}
	started := ws.request(homeID, "recruiting.profile.repair.begin", controlID, payload)
	encoded, _ := json.Marshal(started)
	if nestedStringField(t, started, "profile", "auth_status") != "repairing" ||
		nestedStringField(t, started, "session", "status") != "awaiting_device" ||
		stringField(t, started, "next_action") != "complete_on_authorized_device" ||
		strings.Contains(string(encoded), secretRef) || strings.Contains(string(encoded), "tool:e2e-authorized-browser") {
		t.Fatalf("unsafe or incomplete Profile repair response=%s", encoded)
	}
	replayed := ws.request(homeID, "recruiting.profile.repair.begin", controlID, payload)
	replayedJSON, _ := json.Marshal(replayed)
	if string(replayedJSON) != string(encoded) {
		t.Fatalf("Profile repair command replay changed response: first=%s replay=%s", encoded, replayedJSON)
	}
	if _, terminal, err := ws.tryRequest(homeID, "recruiting.execution.offer", controlID, map[string]any{
		"command_id": "human-must-not-claim-profile-repair", "executor_incarnation": "browser-1",
		"capability": store.ProfileRepairCapability, "profile_id": profileID,
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
	inspected := recovered.request(homeID, "recruiting.profile.get", controlID, map[string]any{"profile_id": profileID})
	inspectedJSON, _ := json.Marshal(inspected)
	if nestedStringField(t, inspected, "profile", "auth_status") != "repairing" ||
		nestedStringField(t, inspected, "latest_repair_session", "status") != "awaiting_device" ||
		strings.Contains(string(inspectedJSON), secretRef) || strings.Contains(string(inspectedJSON), "tool:e2e-authorized-browser") {
		t.Fatalf("restart lost or leaked Profile repair projection=%s", inspectedJSON)
	}
}

func mustJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
