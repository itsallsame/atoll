package narrative

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNextSceneBatchCapsBacklogAtNarrativeArc(t *testing.T) {
	events := make([]fact, 9)
	for index := range events {
		events[index] = fact{EventID: fmt.Sprintf("E%d", index+1), Day: 3 + index/5, Actor: "he_asheng"}
	}
	events[5].Actor = "environment"
	batch := nextSceneBatch(events)
	if len(batch) != 5 || batch[0].EventID != "E1" || batch[4].EventID != "E5" {
		t.Fatalf("batch = %+v", eventIDs(batch))
	}
	if len(events[len(batch):]) != 4 {
		t.Fatal("later authoritative facts were not left queued")
	}
}

func TestChapterPOVUsesObserverCoverageInsteadOfFirstActor(t *testing.T) {
	events := []fact{
		{EventID: "E1", Actor: "he_asheng", Observers: []string{"he_asheng", "shen_yanqiu"}},
		{EventID: "E2", Actor: "lu_jinghe", Observers: []string{"lu_jinghe", "shen_yanqiu"}},
		{EventID: "E3", Actor: "zhou_jiren", Observers: []string{"zhou_jiren", "shen_yanqiu"}},
		{EventID: "E4", Actor: "shen_yanqiu", Observers: []string{"shen_yanqiu"}},
	}
	if got := chapterPOV(events); got != "沈砚秋" {
		t.Fatalf("POV = %q", got)
	}
	if got := nextSceneBatch(events); len(got) != 4 {
		t.Fatalf("fully observable arc was not selected: %v", eventIDs(got))
	}
}

func TestNextSceneBatchWaitsWhenNoPOVCoversArc(t *testing.T) {
	events := []fact{
		{EventID: "E1", Actor: "he_asheng"},
		{EventID: "E2", Actor: "lu_jinghe"},
		{EventID: "E3", Actor: "zhou_jiren"},
		{EventID: "E4", Actor: "shen_yanqiu"},
	}
	if got := nextSceneBatch(events); got != nil {
		t.Fatalf("unobservable arc selected: %v", eventIDs(got))
	}
}

func TestGenericEventExpansionCannotPublishWithoutScenePlan(t *testing.T) {
	events := make([]fact, 0, 5)
	for i := 0; i < 5; i++ {
		events = append(events, fact{
			EventID: "E" + string(rune('1'+i)), Tick: "morning", Location: "南湾七号田",
			Actor: "he_asheng", Participants: []string{"he_asheng", "lu_jinghe"},
			Observers: []string{"shen_yanqiu", "zhou_jiren"}, Action: "重新丈量田界",
			Summary:              "泥面上的量线改变了争议双方能够提出的证据。",
			PerceivedGoalByActor: "保住连续耕作留下的边界", ActualEffect: "复测结果进入册局记录。",
		})
	}
	got := projectChapter(6, 6, events)
	if got.Status != "draft" || got.StartDay != 6 || got.EndDay != 6 || len(got.SourceEvents) != 5 {
		t.Fatalf("chapter metadata=%+v", got)
	}
	if utf8.RuneCountInString(strings.ReplaceAll(got.Markdown, "\n", "")) < 1200 {
		t.Fatalf("chapter remains too short: %d runes", utf8.RuneCountInString(got.Markdown))
	}
	for _, event := range events {
		if !strings.Contains(got.Markdown, event.Summary) || !strings.Contains(got.Markdown, event.ActualEffect) {
			t.Fatalf("chapter lost source fact %s", event.EventID)
		}
	}
	if err := validateProjectedChapter(got, events); err == nil {
		t.Fatal("generic event expansion passed without a scene plan")
	}
}

func TestEvidenceMatrixArcUsesContinuousSceneInsteadOfEventTemplate(t *testing.T) {
	events := []fact{
		{EventID: "D07-E020", RuleID: "post_flood_ground_change", Actor: "environment", Participants: []string{"he_asheng"}, Summary: "泥面变清", ActualEffect: "痕迹可复测"},
		{EventID: "D08-E021", RuleID: "post_flood_ground_change", Actor: "environment", Participants: []string{"he_asheng"}, Summary: "泥面继续变清", ActualEffect: "可见度提高"},
		{EventID: "D08-E022", RuleID: "shen_builds_trace_series", Actor: "shen_yanqiu", Summary: "分层描图", ActualEffect: "形成时间序列"},
		{EventID: "D08-E023", RuleID: "he_challenges_trace_ownership", Actor: "he_asheng", Summary: "区分脚印", ActualEffect: "保存异议"},
		{EventID: "D08-E024", RuleID: "lu_challenges_trace_admissibility", Actor: "lu_jinghe", Summary: "质疑采纳", ActualEffect: "证据分类"},
		{EventID: "D08-E025", RuleID: "zhou_orders_evidence_matrix", Actor: "zhou_jiren", Summary: "形成矩阵", ActualEffect: "送县复核"},
	}
	got := projectChapter(7, 8, events)
	got.Plan = planChapter(7, 8, events)
	got.Review = reviewProjectedChapter(got, got.Plan, events)
	if err := validateProjectedChapter(got, events); err != nil {
		t.Fatalf("continuous scene failed validation: %v", err)
	}
	if !got.Plan.Ready || !got.Review.Pass || got.Plan.Beats[0].Role != "setup" || got.Plan.Beats[len(got.Plan.Beats)-1].Role != "resolution" {
		t.Fatalf("plan/review did not form a publishable arc: plan=%+v review=%+v", got.Plan, got.Review)
	}
	for _, boilerplate := range []string{"发生的事把先前尚可含混的局面", "这一步又压在前一步之上", "目睹或接触到了这次变化"} {
		if strings.Contains(got.Markdown, boilerplate) {
			t.Fatalf("continuous scene retained event-card boilerplate %q", boilerplate)
		}
	}
}

func TestSplitMeasurementArcUsesContinuousScene(t *testing.T) {
	events := []fact{
		{EventID: "D06-E015", RuleID: "post_flood_ground_change", Summary: "泥面变清", ActualEffect: "痕迹可复测"},
		{EventID: "D06-E016", RuleID: "he_measurement_position", Summary: "向东丈量", ActualEffect: "形成东线"},
		{EventID: "D06-E017", RuleID: "lu_measurement_response", Summary: "向西丈量", ActualEffect: "形成西线"},
		{EventID: "D06-E018", RuleID: "shen_records_split_measurement", Summary: "保留两线", ActualEffect: "记录过程"},
		{EventID: "D06-E019", RuleID: "zhou_escalates_measurement_review", Summary: "传唤邻户", ActualEffect: "安排复测"},
	}
	got := projectChapter(6, 6, events)
	if err := validateProjectedChapter(got, events); err != nil {
		t.Fatalf("split-measurement scene failed validation: %v", err)
	}
	if strings.Contains(got.Markdown, "这一步又压在前一步之上") {
		t.Fatal("split-measurement scene retained event-card boilerplate")
	}
}
