package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestInFlightResultDistinguishesDrainFromCancelFence(t *testing.T) {
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

	for _, test := range []struct {
		prefix string
		mode   model.PauseMode
		accept bool
	}{
		{prefix: "scope-drain-result", mode: model.PauseDrain, accept: true},
		{prefix: "scope-cancel-result", mode: model.PauseCancel, accept: false},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			offerAt, _ := prepareListingExecutionWork(t, ctx, repository, test.prefix, 1)
			offer := startListingAttempt(t, ctx, repository, test.prefix, offerAt)
			if offer.Attempt.SourceConfigurationVersion == 0 || offer.Attempt.SourceExecutionFence == 0 {
				t.Fatalf("offer omitted separated Source fences: %+v", offer.Attempt)
			}
			source, err := repository.GetSource(ctx, offer.Occurrence.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			paused, err := source.Pause(source.Version, test.mode)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateSourceCAS(ctx, source.Version, paused, offerAt.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			artifact := mustResultArtifact(t, test.prefix+"-page", model.ArtifactPage,
				offer.Work.WorkID, offer.Attempt.AttemptID)
			outcome, resultErr := repository.AcceptListingPage(ctx, ListingPageResult{
				AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
				ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, PageSequence: 1, Terminal: true,
				Artifact: artifact, ObservedAt: offerAt.Add(2 * time.Second),
			})
			if test.accept {
				if resultErr != nil || outcome.Progress.PageSequence != 1 {
					t.Fatalf("drain rejected valid in-flight result: %+v err=%v", outcome, resultErr)
				}
			} else if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("cancel accepted stale in-flight result: %+v err=%v", outcome, resultErr)
			}
		})
	}
}

func TestDetailResultUsesSeparatedScopeFences(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2095, 2, 12, 4, 0, 0, 0, time.UTC)

	for index, test := range []struct {
		name   string
		change func(model.RecruitmentSource) (model.RecruitmentSource, error)
		accept bool
	}{
		{name: "drain", accept: true, change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseDrain)
		}},
		{name: "cancel", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseCancel)
		}},
		{name: "configuration_change", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.StageEndpoint(source.Version, "https://"+source.SourceID+".example.com/jobs-v2", "")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "detail-scope-fence-" + test.name
			fixture := createDetailFixture(t, ctx, repository, prefix, now.Add(time.Duration(index)*time.Hour))
			if fixture.attempt.SourceConfigurationVersion == 0 || fixture.attempt.SourceExecutionFence == 0 {
				t.Fatalf("offer omitted separated Source fences: %+v", fixture.attempt)
			}
			current, err := repository.GetSource(ctx, fixture.source.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := test.change(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateSourceCAS(ctx, current.Version, changed, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			result := fixture.result(prefix + "-artifact")
			result.ObservedAt = fixture.now.Add(2 * time.Second)
			outcome, resultErr := repository.AcceptDetailResult(ctx, result)
			if test.accept {
				if resultErr != nil || outcome.Job.DetailVersion != 1 {
					t.Fatalf("drain rejected valid in-flight Detail result: %+v err=%v", outcome, resultErr)
				}
				return
			}
			if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("stale Detail result was accepted: %+v err=%v", outcome, resultErr)
			}
			var rejected bool
			if err := db.QueryRowContext(ctx, `SELECT rejected FROM recruiting_artifacts WHERE artifact_id = ?`,
				result.Artifact.ArtifactID).Scan(&rejected); err != nil || !rejected {
				t.Fatalf("rejected Detail Artifact retained=%v err=%v", rejected, err)
			}
		})
	}
}
