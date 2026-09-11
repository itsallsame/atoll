package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

// TestRecruitingRecipeResourceAccessSurvivesRestart proves that Recipe bytes
// remain governed by the Atoll Channel Resource boundary. A second Recruiting
// actor that shares the application database but runs in another user's Home
// Channel cannot resolve a known Resource ID, while the owning actor can still
// propose from the same immutable bytes after a Server restart.
func TestRecruitingRecipeResourceAccessSurvivesRestart(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	owner := newAPIClient(t, h.base)
	ownerRegistration := owner.register("resource-owner", "resource-owner@example.test", "owner-local-password")
	ownerHome := stringField(t, ownerRegistration, "home_channel_id")
	// Home-channel eligibility is materialized asynchronously by the Atoll
	// core; use the same process-level readiness margin as the other new-user
	// recruiting journeys before requesting initial history.
	time.Sleep(2 * time.Second)
	ownerWS := dialWS(t, h.base, owner.cookieHeader(), map[string]int64{ownerHome: 0})
	ownerControl := createResourceAccessControl(t, ownerWS, ownerHome, "owner")
	waitRecruitingReady(t, ownerWS, ownerHome, ownerControl, h.server)

	other := newAPIClient(t, h.base)
	otherRegistration := other.register("resource-other", "resource-other@example.test", "other-local-password")
	otherHome := stringField(t, otherRegistration, "home_channel_id")
	time.Sleep(2 * time.Second)
	otherWS := dialWS(t, h.base, other.cookieHeader(), map[string]int64{otherHome: 0})
	otherControl := createResourceAccessControl(t, otherWS, otherHome, "other")
	waitRecruitingReady(t, otherWS, otherHome, otherControl, h.server)

	source, _, _ := seedRecipeOperations(t, runtimeDSN, time.Now().UTC().Truncate(time.Second))
	spec := recruitingLiveRecipe()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	contentHash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	const contentRef = "recipe://e2e-channel-private-listing"
	ownerWS.resource(map[string]any{"channel_id": ownerHome, "op": "create", "resource_id": contentRef,
		"args": json.RawMessage(raw)})
	if _, err := otherWS.tryResource(map[string]any{"channel_id": otherHome, "op": "read", "resource_id": contentRef}); err == nil {
		t.Fatal("another Home Channel read an owner's Recipe Resource")
	}
	ownerChannel := registrarRequest(t, ownerWS, ownerHome, systemActor, "system.channel.get",
		map[string]any{"channel_id": ownerHome})
	qualifiedOwnerChannel := stringField(t, ownerChannel, "qualified_name")
	const deviceName = "resource-file-host"
	device := registrarRequest(t, ownerWS, ownerHome, systemActor, "system.device.create", map[string]any{"name": deviceName})
	deviceID := stringField(t, device, "id")
	attachDevice(t, ownerWS, ownerHome, deviceID)
	const hostDeclaration = "e2e-resource-file-readiness"
	registrarRequest(t, ownerWS, ownerHome, systemActor, "system.actor.template.create", map[string]any{
		"id": hostDeclaration, "name": hostDeclaration, "class": "echo",
		"description": "Recruiting Artifact File host readiness probe.", "config": map[string]any{}, "visibility": "private",
	})
	hostIntro := ownerWS.request(ownerHome, "system.member.create", systemActor,
		map[string]any{"decl_id": hostDeclaration, "desired_host": deviceID})
	hostActor := stringField(t, hostIntro, "member")
	daemonHome := filepath.Join(h.root, "resource-file-daemon")
	daemonLog := filepath.Join(h.root, "logs", "resource-file-daemon.log")
	daemon := startProc(t, "resource-file-daemon", filepath.Join(e2eBinDir, "atoll-daemon"), []string{
		"--server", fmt.Sprintf("ws://127.0.0.1:%d/compute", h.port), "--key", stringField(t, device, "key"),
		"--name", deviceName, "--home", daemonHome,
	}, h.env, filepath.Join(h.root, "work"), daemonLog)
	waitActorPresenceInChannel(t, ownerWS, ownerHome, hostActor, daemon, daemonLog)
	artifactAddress := "daemon://" + deviceName + "/" + qualifiedOwnerChannel + "/recruiting-artifacts/evidence.bin"
	createdArtifact := ownerWS.resource(map[string]any{"channel_id": ownerHome, "op": "create",
		"address": artifactAddress, "with_content": true})
	artifactBytes := bytes.Repeat([]byte("channel-private-recruiting-evidence\n"), 32)
	httpPutFile(t, owner, h.base, ownerHome, artifactAddress, stringField(t, createdArtifact, "ticket"), artifactBytes)
	if _, err := otherWS.tryResource(map[string]any{"channel_id": otherHome, "op": "read", "resource_id": artifactAddress}); err == nil {
		t.Fatal("another Home Channel read an owner's Recruiting Artifact File")
	}

	h.restartServer()
	owner = newAPIClient(t, h.base)
	if login := owner.login("resource-owner@example.test", "owner-local-password"); login["id"] != "resource-owner" {
		t.Fatalf("owner login after restart=%v", login)
	}
	ownerWS = dialWS(t, h.base, owner.cookieHeader(), map[string]int64{ownerHome: 0})
	waitRecruitingReady(t, ownerWS, ownerHome, ownerControl, h.server)
	waitActorPresenceInChannel(t, ownerWS, ownerHome, hostActor, daemon, daemonLog)
	if _, err := ownerWS.tryResource(map[string]any{"channel_id": ownerHome, "op": "read", "resource_id": contentRef}); err != nil {
		t.Fatalf("owner Recipe Resource was not durable across restart: %v", err)
	}
	if got := httpReadFile(t, owner, h.base, ownerWS, ownerHome, artifactAddress); !bytes.Equal(got, artifactBytes) {
		t.Fatalf("owner Recruiting Artifact File after restart bytes=%d want=%d", len(got), len(artifactBytes))
	}

	other = newAPIClient(t, h.base)
	if login := other.login("resource-other@example.test", "other-local-password"); login["id"] != "resource-other" {
		t.Fatalf("other user login after restart=%v", login)
	}
	otherWS = dialWS(t, h.base, other.cookieHeader(), map[string]int64{otherHome: 0})
	waitRecruitingReady(t, otherWS, otherHome, otherControl, h.server)
	if _, err := otherWS.tryResource(map[string]any{"channel_id": otherHome, "op": "read", "resource_id": contentRef}); err == nil {
		t.Fatal("another Home Channel read an owner's Recipe Resource after restart")
	}
	if _, err := otherWS.tryResource(map[string]any{"channel_id": otherHome, "op": "read", "resource_id": artifactAddress}); err == nil {
		t.Fatal("another Home Channel read an owner's Recruiting Artifact File after restart")
	}

	ownerPayload := map[string]any{
		"command_id": "e2e-resource-owner-proposal", "target": map[string]any{"target_type": "source", "target_id": source.SourceID},
		"expected_version": source.Version, "recipe_id": "e2e-resource-owner-candidate", "recipe_version": 1,
		"endpoint_revision": source.ActiveEndpoint.Revision, "content_ref": contentRef,
		"expected_content_hash": contentHash, "reason": "propose from the durable Channel-owned Recipe Resource",
	}
	proposed := ownerWS.request(ownerHome, "recruiting.recipe.propose", ownerControl, ownerPayload)
	if nestedStringField(t, proposed, "recipe", "status") != "draft" {
		t.Fatalf("owner Recipe proposal=%v", proposed)
	}

	otherPayload := map[string]any{
		"command_id": "e2e-resource-other-proposal", "target": map[string]any{"target_type": "source", "target_id": source.SourceID},
		"expected_version": source.Version, "recipe_id": "e2e-resource-other-candidate", "recipe_version": 1,
		"endpoint_revision": source.ActiveEndpoint.Revision, "content_ref": contentRef,
		"expected_content_hash": contentHash, "reason": "prove a known Resource ID does not bypass Channel access",
	}
	if _, terminal, err := otherWS.tryRequest(otherHome, "recruiting.recipe.propose", otherControl, otherPayload); err == nil ||
		terminal["error_code"] != "quality_rejected" {
		t.Fatalf("cross-Channel Recipe proposal terminal=%v err=%v", terminal, err)
	}

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var recipes, receipts int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_recipes WHERE recipe_id = 'e2e-resource-other-candidate'),
  (SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = 'e2e-resource-other-proposal')`).
		Scan(&recipes, &receipts); err != nil {
		t.Fatal(err)
	}
	if recipes != 0 || receipts != 0 {
		t.Fatalf("denied cross-Channel proposal leaked facts: recipes=%d receipts=%d", recipes, receipts)
	}
}

func createResourceAccessControl(t *testing.T, ws *wsClient, homeID, suffix string) string {
	t.Helper()
	declaration := "e2e-resource-access-" + suffix
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": declaration, "name": declaration, "class": "recruiting",
		"description": "Recruiting Resource access boundary control.",
		"config": map[string]any{"executor_id": "unused-resource-access-executor-" + suffix,
			"reconcile_interval_ms": 30000, "daily_schedule_enabled": false},
		"visibility": "private",
	})
	introduced := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": declaration})
	return stringField(t, introduced, "member")
}
