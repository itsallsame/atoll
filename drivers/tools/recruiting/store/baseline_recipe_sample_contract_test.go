package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestBaselineRecipeSamplesDoNotStarveLaterSources(t *testing.T) {
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
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2089, 9, 1, 0, 0, 0, 0, time.UTC)

	createBaseline := func(index int) string {
		companyID := fmt.Sprintf("sample-fairness-company-%d", index)
		sourceID := fmt.Sprintf("sample-fairness-source-%d", index)
		recipeID := fmt.Sprintf("sample-fairness-listing-%d", index)
		host := fmt.Sprintf("sample-fairness-%d.example.com", index)
		company, _ := model.NewCompany(companyID, companyID, "https://"+host)
		if err := repository.CreateCompany(ctx, company, now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
		recipe := activeRecipe(t, recipeID, model.RecipeListing, host, 1, recipeID+"-contract")
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
		source, _ := model.NewRecruitmentSource(sourceID, companyID, "https://"+host+"/jobs", "all", 1)
		if err := repository.CreateSource(ctx, source, now); err != nil {
			t.Fatal(err)
		}
		validating, _ := source.BeginValidation(source.Version)
		if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
			t.Fatal(err)
		}
		assignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing,
			recipe.RecipeID, recipe.Version, recipe.ContractHash, now.Format(time.RFC3339Nano))
		ready, err := validating.PublishValidated(validating.Version, assignment,
			verifiedStoreAssessment(validating, assignment, now))
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
			t.Fatal(err)
		}
		baseline, _ := model.NewBaselineGeneration(sourceID, 1)
		if err := repository.CreateBaseline(ctx, baseline, now); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("job-%d", index)
		observation, _ := model.NewListingObservation(model.ListingObservation{
			ObservationID: "observation-" + key, OccurrenceID: "baseline-" + key,
			SourceID: sourceID, SourceJobKey: key, DetailURL: "https://" + host + "/jobs/" + key,
			ActivityAt: now.Format(time.RFC3339Nano), ListingFingerprint: "sha256:" + key,
			RecipeID: recipeID, RecipeVersion: 1, ArtifactID: "artifact-" + key,
		})
		value, _ := json.Marshal(observation)
		if _, err := repository.StageBaselineRows(ctx, sourceID, 1, []BaselineStageRow{{
			SourceJobKey: key, ObservationID: observation.ObservationID, Value: value,
		}}, now); err != nil {
			t.Fatal(err)
		}
		finalized, _ := baseline.FinalizeListing(baseline.Version, 1)
		checkpoint, _ := model.EstablishCheckpoint(model.IncrementalCheckpoint{
			SourceID: sourceID, RecipeID: recipeID, RecipeVersion: 1, ContractHash: recipe.ContractHash,
			Strategy: model.CheckpointActivityTime, FrontierActivityAt: now.Format(time.RFC3339Nano),
			OverlapPages: 1, LastOccurrenceID: "baseline-" + key,
		})
		if err := repository.FinalizeBaselineListing(ctx, baseline.Version, finalized, checkpoint, now); err != nil {
			t.Fatal(err)
		}
		return sourceID
	}

	firstSource := createBaseline(1)
	secondSource := createBaseline(2)
	first, err := repository.MaterializeNextBaselineRecipeSample(ctx, now.Add(time.Minute))
	if err != nil || first == nil || first.SourceID != firstSource {
		t.Fatalf("first sample=%+v err=%v", first, err)
	}
	second, err := repository.MaterializeNextBaselineRecipeSample(ctx, now.Add(2*time.Minute))
	if err != nil || second == nil || second.SourceID != secondSource {
		t.Fatalf("second sample=%+v err=%v", second, err)
	}
	third, err := repository.MaterializeNextBaselineRecipeSample(ctx, now.Add(3*time.Minute))
	if err != nil || third != nil {
		t.Fatalf("sample replay=%+v err=%v", third, err)
	}
}
