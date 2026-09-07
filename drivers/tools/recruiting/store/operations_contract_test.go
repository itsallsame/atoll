package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestProfileBudgetAndRepairRepositoryContracts(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repository, _ := NewRepository(db)
	now := time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC)

	profile, _ := model.NewBrowserProfile("operations-profile", "operations.example.com", "device-a", "secret://operations/v1")
	if err := repository.CreateProfile(ctx, profile, now); err != nil {
		t.Fatal(err)
	}
	repairing, _ := profile.BeginRepair(profile.Version)
	if err := repository.UpdateProfileCAS(ctx, profile.Version, repairing, now); err != nil {
		t.Fatal(err)
	}
	storedProfile, err := repository.GetProfile(ctx, profile.ProfileID)
	if err != nil || storedProfile.SecretRef != "secret://operations/v1" || storedProfile.AuthStatus != model.ProfileRepairing {
		t.Fatalf("stored profile = %+v %v", storedProfile, err)
	}
	if err := repository.UpdateProfileCAS(ctx, profile.Version, repairing, now); err == nil {
		t.Fatal("stale profile update was accepted")
	}

	permit, _ := model.NewBudgetPermit("operations-permit", "operations-attempt", "operations.example.com", profile.ProfileID, "browser.recipe", "operations-company", 1)
	if err := repository.CreateBudgetPermit(ctx, permit, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	released, _ := permit.Release(permit.Version)
	permitErrors := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			permitErrors <- repository.UpdateBudgetPermitCAS(ctx, permit.Version, released)
		}()
	}
	group.Wait()
	close(permitErrors)
	var permitSucceeded, permitConflicted int
	for updateErr := range permitErrors {
		if updateErr == nil {
			permitSucceeded++
		} else {
			var conflict *model.VersionConflictError
			if errors.As(updateErr, &conflict) {
				permitConflicted++
			} else {
				t.Fatalf("permit CAS = %v", updateErr)
			}
		}
	}
	if permitSucceeded != 1 || permitConflicted != 1 {
		t.Fatalf("permit succeeded=%d conflicted=%d", permitSucceeded, permitConflicted)
	}

	for _, id := range []string{"repair-work-a", "repair-work-b"} {
		work, _ := model.NewWork(id, "source", "repair-source", "repair", "failure")
		if err := repository.CreateWork(ctx, work, WorkPlacement{BusinessKey: "business-" + id, Priority: 1, Capability: "repair", NotBefore: now}, now); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := model.NewRepairIncident("repair-incident-a", model.FailureOrigin, "operations.example.com", "http_403", "policy-1", "repair-work-a")
	opened, joined, err := repository.OpenOrJoinRepair(ctx, first, now)
	if err != nil || joined || opened.IncidentID != first.IncidentID {
		t.Fatalf("open repair = %+v joined=%v err=%v", opened, joined, err)
	}
	second, _ := model.NewRepairIncident("repair-incident-b", model.FailureOrigin, "operations.example.com", "http_403", "policy-1", "repair-work-b")
	joinedIncident, joined, err := repository.OpenOrJoinRepair(ctx, second, now.Add(time.Second))
	if err != nil || !joined || joinedIncident.IncidentID != first.IncidentID || len(joinedIncident.AffectedWorkIDs) != 2 {
		t.Fatalf("join repair = %+v joined=%v err=%v", joinedIncident, joined, err)
	}
	var incidents, affected int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_repair_incidents WHERE repair_key = ?", first.RepairKey).Scan(&incidents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_repair_affected_works WHERE incident_id = ?", first.IncidentID).Scan(&affected); err != nil {
		t.Fatal(err)
	}
	if incidents != 1 || affected != 2 {
		t.Fatalf("repair single-flight incidents=%d affected=%d", incidents, affected)
	}
}
