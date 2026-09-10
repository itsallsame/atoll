package recruiting

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestParseConfigDefaultsAndRejectsUnknownFields(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil || cfg.ExecutorID != "recruiting-executor" || cfg.DatabaseDSNEnv != "ATOLL_RECRUITING_MYSQL_DSN" || cfg.ReconcileIntervalMS != 30_000 ||
		!cfg.DailyScheduleEnabled || cfg.DailyScheduleTimezone != "UTC" || cfg.DailyCutoffLocal != "00:00:00" ||
		cfg.AttemptStaleAfterMS != 900_000 || cfg.AttemptRecoveryLimit != 100 || cfg.DailyWindowDurationMinutes != 480 ||
		cfg.DailySchedulePolicyVersion != 1 || cfg.DailyWorkMaterializeLimit != 100 || cfg.CompanyImportApplyLimit != 100 || cfg.BudgetPolicyVersion != 1 ||
		cfg.BudgetMaxActive != 1_000 || cfg.BudgetMaxPerOrigin != 8 || cfg.BudgetMaxPerProfile != 1 || cfg.BudgetPermitTTLMS != 900_000 ||
		cfg.RetryPolicyVersion != 1 || cfg.RetryMaxAutomaticAttempts != 4 || cfg.RetryBaseDelayMS != 30_000 ||
		cfg.RetryMaxDelayMS != 1_800_000 || cfg.RetryThrottledDelayMS != 300_000 {
		t.Fatalf("default config = %+v, %v", cfg, err)
	}
	if _, err := parseConfig(json.RawMessage(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown config field was accepted")
	}
	if _, err := parseConfig(json.RawMessage(`{"executor_id":" "}`)); err == nil {
		t.Fatal("blank executor identity was accepted")
	}
	if _, err := parseConfig(json.RawMessage(`{"database_dsn_env":" "}`)); err == nil {
		t.Fatal("blank database DSN environment name was accepted")
	}
	if _, err := parseConfig(json.RawMessage(`{"reconcile_interval_ms":99}`)); err == nil {
		t.Fatal("too-small reconcile interval was accepted")
	}
	fleet, err := parseConfig(json.RawMessage(`{"executors":[{"actor_id":" tool:executor-a ","capability":"http.fetch"},{"actor_id":"tool:executor-b:123","capability":"browser.recipe"}]}`))
	if err != nil || len(fleet.Executors) != 2 || fleet.Executors[0].ActorID != "tool:executor-a" || fleet.Executors[1].Capability != "browser.recipe" {
		t.Fatalf("executor fleet = %+v err=%v", fleet.Executors, err)
	}
	if _, err := parseConfig(json.RawMessage(`{"executors":[{"actor_id":"tool:executor-a","capability":"http.fetch"},{"actor_id":"tool:executor-a","capability":"browser.recipe"}]}`)); err == nil {
		t.Fatal("duplicate executor actor was accepted")
	}
	if _, err := parseConfig(json.RawMessage(`{"executors":[{"actor_id":"executor-a","capability":"http.fetch"}]}`)); err == nil {
		t.Fatal("unqualified executor target was accepted")
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"daily_schedule_timezone":"Not/AZone"}`),
		json.RawMessage(`{"daily_cutoff_local":"24:00:00"}`),
		json.RawMessage(`{"daily_window_duration_minutes":0}`),
		json.RawMessage(`{"daily_schedule_policy_version":0}`),
		json.RawMessage(`{"daily_work_materialize_limit":501}`),
		json.RawMessage(`{"attempt_stale_after_ms":999}`),
		json.RawMessage(`{"attempt_recovery_limit":501}`),
		json.RawMessage(`{"budget_max_active":0}`),
		json.RawMessage(`{"budget_max_active":4,"budget_max_per_origin":5}`),
		json.RawMessage(`{"budget_permit_ttl_ms":999}`),
		json.RawMessage(`{"retry_policy_version":0}`),
		json.RawMessage(`{"retry_max_automatic_attempts":101}`),
		json.RawMessage(`{"retry_base_delay_ms":999}`),
		json.RawMessage(`{"retry_base_delay_ms":60000,"retry_max_delay_ms":30000}`),
		json.RawMessage(`{"retry_base_delay_ms":60000,"retry_throttled_delay_ms":30000}`),
	} {
		if _, err := parseConfig(raw); err == nil {
			t.Fatalf("invalid daily config was accepted: %s", raw)
		}
	}
}

func TestExecutionDispatchTargetIsCapabilityBoundAndDeterministic(t *testing.T) {
	cfg := Config{Executors: []ExecutorTargetConfig{
		{ActorID: "tool:executor-http-b", Capability: "http.fetch"},
		{ActorID: "tool:executor-browser", Capability: "browser.recipe"},
		{ActorID: "tool:executor-http-a", Capability: "http.fetch"},
	}}
	first, found := cfg.executionDispatchTarget("http.fetch", "command-1\nwork-1")
	if !found || first.Capability != "http.fetch" || (first.ActorID != "tool:executor-http-a" && first.ActorID != "tool:executor-http-b") {
		t.Fatalf("selected HTTP target = %+v found=%v", first, found)
	}
	for index := 0; index < 10; index++ {
		next, nextFound := cfg.executionDispatchTarget("http.fetch", "command-1\nwork-1")
		if !nextFound || next != first {
			t.Fatalf("selection changed: first=%+v next=%+v", first, next)
		}
	}
	if _, found := cfg.executionDispatchTarget("document.parse", "command-1\nwork-1"); found {
		t.Fatal("selected an executor for an unsupported capability")
	}
}

func TestRecipeValidationCreatesCapabilityBoundExecutionDispatch(t *testing.T) {
	cfg := Config{Executors: []ExecutorTargetConfig{{ActorID: "tool:executor-http", Capability: "http.fetch"}}}
	work, err := model.NewWork("recipe-validation-work", "recipe", "listing@2", "recipe_validation", "manual")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2095, 2, 1, 0, 0, 0, 0, time.UTC)
	placement := store.WorkPlacement{Capability: "http.fetch", Origin: "https://jobs.example.com", NotBefore: now}
	dispatch, err := workCommandDispatch(cfg, work, placement, "validate-recipe-command", "recipe_validation")
	if err != nil || dispatch == nil || dispatch.TargetActorID != "tool:executor-http" ||
		dispatch.Capability != placement.Capability || dispatch.CauseKind != "recipe_validation" {
		t.Fatalf("Recipe validation dispatch=%+v err=%v", dispatch, err)
	}
}

func TestManifestExposesControlAndExecutorResultWords(t *testing.T) {
	words := manifest().Words
	for _, word := range []string{
		TypeProbeStart, TypeProbeSchedule, TypeProbeStatus, TypeExecutionResult,
		TypeCompanyImport, TypeCompanyImportGet, TypeCompanyImportItems, TypeCompanyImportConfirm, TypeCompanyImportCancel,
		TypeCompanyAdd, TypeCompanyUpdate, TypeCompanyPause, TypeCompanyResume,
		TypeCompanyArchive, TypeCompanyRestore, TypeCompanyGet, TypeCompanyList,
		TypeSourceAdd, TypeSourceUpdate, TypeSourceValidate, TypeSourceValidationPublish, TypeSourceValidationReject, TypeSourcePause,
		TypeSourceResume, TypeSourceArchive, TypeSourceRestore, TypeSourceGet, TypeSourceList,
		TypeRunDiagnostic,
		TypeRunProduction,
		TypeRecipeInspect, TypeRecipeValidate, TypeRecipeApprove, TypeRecipeRollout, TypeRecipeQuarantine, TypeRecipeRollback,
		TypeJobGet, TypeJobList, TypeWorkGet, TypeWorkList, TypeDailyRunGet, TypeDailyRunList, TypeDailyRunSummary,
		TypeWorkCreate, TypeWorkPause, TypeWorkResume, TypeWorkRetry, TypeWorkCancel, TypeWorkResolve,
		TypeSystemReconcile, TypeExecutionWakeCompleted,
	} {
		if _, ok := words[word]; !ok {
			t.Fatalf("manifest does not expose %q", word)
		}
	}
}
