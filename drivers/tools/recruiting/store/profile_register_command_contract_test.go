package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestProfileRegisterIsAtomicIdempotentAndSecretFree(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)
	profileID := "profile-register-contract"
	profile, err := model.NewUnprovisionedBrowserProfile(profileID, "jobs.example.test", "tool:profile-device",
		"secret://local-browser-profile/profile-register-contract/v1", testProfileVerificationRecipe("jobs.example.test", "register"))
	if err != nil {
		t.Fatal(err)
	}
	incident, _ := model.NewProfileProvisioningIncident("profile-register-incident", profileID, "profile-register-work")
	work, _ := model.NewWork(incident.RepairWorkID, "repair_incident", incident.IncidentID, "repair", "human")
	work, _ = work.WithCausality("human:operator", "message-register", "")
	placement := WorkPlacement{BusinessKey: "repair|profile-provision|" + profileID, Priority: 500, NotBefore: now}
	response, _ := json.Marshal(map[string]any{"profile_id": profileID, "auth_status": profile.AuthStatus,
		"repair_incident_id": incident.IncidentID, "repair_work_id": work.WorkID})
	receipt, _ := model.NewCommandReceipt("profile-register-command", "recruiting.profile.register",
		"sha256:profile-register-request", response)
	profileAudit, _ := json.Marshal(map[string]any{"profile_id": profileID, "requested_by": "human:operator"})
	repairAudit, _ := json.Marshal(map[string]any{"profile_id": profileID, "repair_work_id": work.WorkID})
	profileEvent, _ := model.NewEventIntent("profile-register-event", "profile.registered", "profile", profileID,
		profile.Version, now.Format(time.RFC3339Nano), receipt.CommandID, profileAudit)
	repairEvent, _ := model.NewEventIntent("profile-register-repair-event", "repair.opened", "repair_incident",
		incident.IncidentID, incident.Version, now.Format(time.RFC3339Nano), receipt.CommandID, repairAudit)
	result, err := repository.ApplyProfileRegisterCommand(ctx, profile, incident, work, placement, receipt,
		profileEvent, repairEvent, now)
	if err != nil || result.Replayed {
		t.Fatalf("register result=%+v err=%v", result, err)
	}
	replay, err := repository.ApplyProfileRegisterCommand(ctx, profile, incident, work, placement, receipt,
		profileEvent, repairEvent, now)
	if err != nil || !replay.Replayed || string(replay.Response) != string(result.Response) {
		t.Fatalf("register replay=%+v err=%v", replay, err)
	}
	storedProfile, err := repository.GetProfile(ctx, profileID)
	if err != nil || storedProfile.AuthStatus != model.ProfileRepairing || storedProfile.Version != 2 {
		t.Fatalf("stored Profile=%+v err=%v", storedProfile, err)
	}
	storedIncident, err := repository.GetRepairIncidentAggregate(ctx, incident.IncidentID)
	if err != nil || len(storedIncident.AffectedWorkIDs) != 0 || storedIncident.RepairWorkID != work.WorkID ||
		storedIncident.Status != model.RepairOpen {
		t.Fatalf("stored incident=%+v err=%v", storedIncident, err)
	}
	storedWork, err := repository.GetWorkRecord(ctx, work.WorkID)
	if err != nil || storedWork.Work.Status != model.WorkOpen || storedWork.Placement.BusinessKey != placement.BusinessKey ||
		storedWork.Placement.Capability != "" || storedWork.Placement.ProfileID != "" {
		t.Fatalf("stored repair Work=%+v err=%v", storedWork, err)
	}
	for label, raw := range map[string][]byte{"response": result.Response, "profile event": profileEvent.Payload, "repair event": repairEvent.Payload} {
		if strings.Contains(string(raw), profile.SecretRef) || strings.Contains(string(raw), profile.DeviceID) {
			t.Fatalf("%s leaked private Profile reference", label)
		}
	}
	duplicate := profile
	duplicate.ProfileID = profileID
	conflictReceipt, _ := model.NewCommandReceipt("profile-register-conflict", receipt.Word,
		"sha256:profile-register-conflict", response)
	profileEvent.CauseCommandID, repairEvent.CauseCommandID = conflictReceipt.CommandID, conflictReceipt.CommandID
	_, err = repository.ApplyProfileRegisterCommand(ctx, duplicate, incident, work, placement, conflictReceipt,
		profileEvent, repairEvent, now)
	if !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("duplicate Profile error=%v", err)
	}
	if _, found, lookupErr := repository.LookupCommand(ctx, conflictReceipt.CommandID, conflictReceipt.RequestHash); lookupErr != nil || found {
		t.Fatalf("failed atomic command retained receipt found=%v err=%v", found, lookupErr)
	}
	var affected int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_repair_affected_works WHERE incident_id = ?", incident.IncidentID).Scan(&affected); err != nil || affected != 0 {
		t.Fatalf("provisioning fabricated affected Work count=%d err=%v", affected, err)
	}
}
