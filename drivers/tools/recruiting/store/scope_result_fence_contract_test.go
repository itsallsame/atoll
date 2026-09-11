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
