package recruiting

import (
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/protocol/actor"
)

func TestExecutableWorkPurposesCreateCapabilityDispatches(t *testing.T) {
	cfg := Config{Executors: []ExecutorTargetConfig{{ActorID: actor.ActorID("tool:executor-http"), Capability: "http.fetch"}}}
	placement := store.WorkPlacement{Capability: "http.fetch", Origin: "https://jobs.example.test", NotBefore: time.Now().UTC()}
	for _, purpose := range []string{"listing_sync", "source_validation", "detail_sync", "source_discovery", "baseline_listing"} {
		work, err := model.NewWork("work-"+purpose, "source", "source-1", purpose, "human")
		if err != nil {
			t.Fatal(err)
		}
		dispatch, err := workCommandDispatch(cfg, work, placement, "command-"+purpose, purpose+"_created")
		if err != nil || dispatch == nil || dispatch.TargetActorID != "tool:executor-http" || dispatch.Capability != "http.fetch" {
			t.Fatalf("purpose %q dispatch = %+v, %v", purpose, dispatch, err)
		}
	}
	nonExecutable, _ := model.NewWork("work-repair", "source", "source-1", "repair", "human")
	if dispatch, err := workCommandDispatch(cfg, nonExecutable, placement, "command-repair", "work_created"); err != nil || dispatch != nil {
		t.Fatalf("non-executable repair dispatch = %+v, %v", dispatch, err)
	}
}

func TestManualWorkPlacementIsBoundedAndCanonical(t *testing.T) {
	now := time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC)
	placement, err := manualWorkPlacement("http.fetch", "jobs.example.com", "", 50, "", "2026-09-08T09:00:00Z", now)
	if err != nil {
		t.Fatal(err)
	}
	if !placement.NotBefore.Equal(now) || placement.DeadlineAt == nil || placement.Priority != 50 {
		t.Fatalf("manual placement = %+v", placement)
	}
	for name, fixture := range map[string]struct {
		capability string
		origin     string
		priority   int
	}{
		"missing capability": {origin: "jobs.example.com"},
		"capability spaces":  {capability: "http fetch", origin: "jobs.example.com"},
		"origin port":        {capability: "http.fetch", origin: "jobs.example.com:443"},
		"priority overflow":  {capability: "http.fetch", origin: "jobs.example.com", priority: 1001},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manualWorkPlacement(fixture.capability, fixture.origin, "", fixture.priority, "", "", now); err == nil {
				t.Fatal("unsafe work placement was accepted")
			}
		})
	}
	if _, _, err := retrySchedule("2026-09-08T10:00:00Z", "2026-09-08T09:00:00Z", now); err == nil {
		t.Fatal("deadline before not_before was accepted")
	}
}

func TestManualWorkPurposeAndResolutionAuthority(t *testing.T) {
	if err := validateManualWorkPurpose(Target{Type: "source", ID: "source-1"}, "repair"); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		target  Target
		purpose string
	}{
		{Target{Type: "source", ID: "source-1"}, "listing_sync"},
		{Target{Type: "job", ID: "job-1"}, "detail_sync"},
		{Target{Type: "source", ID: "source-1"}, "detail_sync"},
		{Target{Type: "job", ID: "job-1"}, "listing_sync"},
		{Target{Type: "source", ID: "source-1"}, "arbitrary"},
	} {
		if err := validateManualWorkPurpose(fixture.target, fixture.purpose); err == nil {
			t.Fatalf("invalid manual work accepted: %+v", fixture)
		}
	}
	work, _ := model.NewWork("waiting-work", "source", "source-1", "repair", "manual")
	work, _ = work.Start(work.Version)
	work, _ = work.WaitHuman(work.Version, "operator decision")
	if _, err := resolveWorkHuman(work, work.Version, model.ResolutionSucceeded, "human:alice", "looks good"); err == nil {
		t.Fatal("human resolution asserted executor success")
	}
	resolved, err := resolveWorkHuman(work, work.Version, model.ResolutionAcceptedGap, "human:alice", "source is intentionally unsupported")
	if err != nil || resolved.Status != model.WorkCompleted || resolved.ResolutionActorID != "human:alice" {
		t.Fatalf("accepted gap resolution = %+v %v", resolved, err)
	}
}

func TestWorkControlWordsAndEventsAreExposed(t *testing.T) {
	words := map[string]string{
		TypeWorkPause: "work.paused", TypeWorkResume: "work.resumed",
		TypeWorkCancel: "work.canceled", TypeWorkResolve: "work.completed",
	}
	actorManifest := manifest()
	for word, event := range words {
		if actual := workEventKind(word); actual != event {
			t.Fatalf("event for %s = %s", word, actual)
		}
	}
	for _, word := range []string{TypeWorkCreate, TypeWorkPause, TypeWorkResume, TypeWorkRetry, TypeWorkCancel, TypeWorkResolve, TypeRunJoinOccurrence} {
		if _, exists := actorManifest.Words[word]; !exists {
			t.Fatalf("work control word %s is absent from manifest", word)
		}
	}
}
