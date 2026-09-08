package recruiting

import (
	"encoding/json"
	"testing"
)

func TestParseConfigDefaultsAndRejectsUnknownFields(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil || cfg.ExecutorID != "recruiting-executor" || cfg.DatabaseDSNEnv != "ATOLL_RECRUITING_MYSQL_DSN" || cfg.ReconcileIntervalMS != 30_000 ||
		!cfg.DailyScheduleEnabled || cfg.DailyScheduleTimezone != "UTC" || cfg.DailyCutoffLocal != "00:00:00" ||
		cfg.AttemptStaleAfterMS != 900_000 || cfg.AttemptRecoveryLimit != 100 || cfg.DailyWindowDurationMinutes != 480 ||
		cfg.DailySchedulePolicyVersion != 1 || cfg.DailyWorkMaterializeLimit != 100 || cfg.BudgetPolicyVersion != 1 ||
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

func TestManifestExposesControlAndExecutorResultWords(t *testing.T) {
	words := manifest().Words
	for _, word := range []string{
		TypeProbeStart, TypeProbeSchedule, TypeProbeStatus, TypeExecutionResult,
		TypeCompanyAdd, TypeCompanyUpdate, TypeCompanyPause, TypeCompanyResume,
		TypeCompanyArchive, TypeCompanyRestore, TypeCompanyGet, TypeCompanyList,
		TypeSourceAdd, TypeSourceUpdate, TypeSourceValidate, TypeSourcePause,
		TypeSourceResume, TypeSourceArchive, TypeSourceRestore, TypeSourceGet, TypeSourceList,
		TypeJobGet, TypeJobList, TypeWorkGet, TypeWorkList, TypeDailyRunGet, TypeDailyRunList, TypeDailyRunSummary,
		TypeWorkCreate, TypeWorkPause, TypeWorkResume, TypeWorkRetry, TypeWorkCancel, TypeWorkResolve,
		TypeSystemReconcile,
	} {
		if _, ok := words[word]; !ok {
			t.Fatalf("manifest does not expose %q", word)
		}
	}
}
