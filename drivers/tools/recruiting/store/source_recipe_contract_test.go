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

func TestSourceEndpointAndAssignmentPublishAtomically(t *testing.T) {
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
	now := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("assignment-company", "Assignment", "https://assignment.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("assignment-source", company.CompanyID, "https://assignment.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}

	listingRecipe := activeRecipe(t, "assignment-listing-v1", model.RecipeListing, "assignment.example.com", 1, "listing-contract")
	if err := repository.CreateRecipe(ctx, listingRecipe, now); err != nil {
		t.Fatal(err)
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing, listingRecipe.RecipeID, listingRecipe.Version, listingRecipe.ContractHash, now.Format(time.RFC3339))
	ready, _ := validating.PublishValidated(validating.Version, listingAssignment)
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	storedSource, err := repository.GetSource(ctx, source.SourceID)
	if err != nil || storedSource.ReadinessStatus != model.SourceReady || storedSource.ActiveEndpoint == nil || storedSource.CandidateEndpoint != nil {
		t.Fatalf("published source = %+v %v", storedSource, err)
	}
	storedAssignment, err := repository.GetAssignment(ctx, source.SourceID, model.RecipeListing)
	if err != nil || storedAssignment != listingAssignment {
		t.Fatalf("published listing assignment = %+v %v", storedAssignment, err)
	}

	detailV1 := activeRecipe(t, "assignment-detail-v1", model.RecipeDetail, "assignment.example.com", 1, "detail-contract")
	if err := repository.CreateRecipe(ctx, detailV1, now); err != nil {
		t.Fatal(err)
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail, detailV1.RecipeID, detailV1.Version, detailV1.ContractHash, now.Format(time.RFC3339))
	withDetail, _ := ready.AssignRecipe(ready.Version, detailAssignment, false)
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	detailV2 := activeRecipe(t, "assignment-detail-v2", model.RecipeDetail, "assignment.example.com", 2, "detail-contract")
	if err := repository.CreateRecipe(ctx, detailV2, now); err != nil {
		t.Fatal(err)
	}
	replacement, _ := detailAssignment.Replace(detailAssignment.AssignmentVersion, detailV2.RecipeID, detailV2.Version, detailV2.ContractHash, now.Add(3*time.Second).Format(time.RFC3339))
	rolled, _ := withDetail.AssignRecipe(withDetail.Version, replacement, false)
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- repository.PublishSourceAssignment(ctx, withDetail.Version, detailAssignment.AssignmentVersion, rolled, replacement, now.Add(3*time.Second))
		}()
	}
	group.Wait()
	close(results)
	var succeeded, conflicted int
	for publishErr := range results {
		if publishErr == nil {
			succeeded++
			continue
		}
		var conflict *model.VersionConflictError
		if errors.As(publishErr, &conflict) || errors.Is(publishErr, ErrAssignmentConflict) {
			conflicted++
			continue
		}
		t.Fatalf("rollout error = %v", publishErr)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("rollout succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	storedAssignment, err = repository.GetAssignment(ctx, source.SourceID, model.RecipeDetail)
	if err != nil || storedAssignment.AssignmentVersion != 2 || storedAssignment.RecipeID != detailV2.RecipeID {
		t.Fatalf("rolled detail assignment = %+v %v", storedAssignment, err)
	}
	currentSource, err := repository.GetSource(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	staleNext, _ := currentSource.AssignRecipe(currentSource.Version, storedAssignment, false)
	if err := repository.PublishSourceAssignment(ctx, currentSource.Version, 1, staleNext, storedAssignment, now.Add(4*time.Second)); !errors.Is(err, ErrAssignmentConflict) {
		t.Fatalf("stale assignment CAS = %v", err)
	}
	afterRollback, err := repository.GetSource(ctx, source.SourceID)
	if err != nil || afterRollback.Version != currentSource.Version {
		t.Fatalf("assignment conflict left source half-updated: %+v %v", afterRollback, err)
	}
}

func activeRecipe(t *testing.T, id string, kind model.RecipeKind, scope string, version uint64, contract string) model.Recipe {
	t.Helper()
	recipe, err := model.NewRecipe(id, kind, scope, version, "content-"+id, contract)
	if err != nil {
		t.Fatal(err)
	}
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, err = recipe.Publish(recipe.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	return recipe
}
