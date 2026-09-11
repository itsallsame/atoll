package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestPausedScopeCannotStarveEligibleExecutionCandidates(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "scope-starvation", 2)
	pausedSourceID := "scope-starvation-source-0"
	source, err := repository.GetSource(ctx, pausedSourceID)
	if err != nil {
		t.Fatal(err)
	}
	paused, _ := source.Pause(source.Version, model.PauseDrain)
	if err := repository.UpdateSourceCAS(ctx, source.Version, paused, offerAt); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "scope-starvation-attempt", ExecutorActorID: "scope-starvation-executor",
		ExecutorIncarnation: "scope-starvation-boot", Capability: "http.fetch", OfferedAt: offerAt,
		BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil {
		t.Fatalf("paused candidate starved eligible Work: %v", err)
	}
	if offer.Occurrence == nil || offer.Occurrence.SourceID == pausedSourceID {
		t.Fatalf("paused Source was offered instead of eligible peer: %+v", offer.Occurrence)
	}
}
