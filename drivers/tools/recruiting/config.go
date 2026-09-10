package recruiting

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/protocol/actor"
)

const Class = "recruiting"

const DefaultActorID actor.ActorID = "recruiting"

type Config struct {
	ExecutorID                   actor.ActorID          `json:"executor_id"`
	Executors                    []ExecutorTargetConfig `json:"executors,omitempty"`
	DatabaseDSNEnv               string                 `json:"database_dsn_env"`
	ReconcileIntervalMS          int                    `json:"reconcile_interval_ms"`
	AttemptStaleAfterMS          int                    `json:"attempt_stale_after_ms"`
	AttemptRecoveryLimit         int                    `json:"attempt_recovery_limit"`
	DailyScheduleEnabled         bool                   `json:"daily_schedule_enabled"`
	DailyScheduleTimezone        string                 `json:"daily_schedule_timezone"`
	DailyCutoffLocal             string                 `json:"daily_cutoff_local"`
	DailyWindowStartDelayMinutes int                    `json:"daily_window_start_delay_minutes"`
	DailyWindowDurationMinutes   int                    `json:"daily_window_duration_minutes"`
	DailySchedulePolicyVersion   uint64                 `json:"daily_schedule_policy_version"`
	DailyWorkMaterializeLimit    int                    `json:"daily_work_materialize_limit"`
	CompanyImportApplyLimit      int                    `json:"company_import_apply_limit"`
	BaselineMaterializeLimit     int                    `json:"baseline_materialize_limit"`
	BudgetPolicyVersion          uint64                 `json:"budget_policy_version"`
	BudgetMaxActive              int                    `json:"budget_max_active"`
	BudgetMaxPerCapability       int                    `json:"budget_max_per_capability"`
	BudgetMaxPerOrigin           int                    `json:"budget_max_per_origin"`
	BudgetMaxPerCompany          int                    `json:"budget_max_per_company"`
	BudgetMaxPerProfile          int                    `json:"budget_max_per_profile"`
	BudgetPermitTTLMS            int                    `json:"budget_permit_ttl_ms"`
	RetryPolicyVersion           uint64                 `json:"retry_policy_version"`
	RetryMaxAutomaticAttempts    int                    `json:"retry_max_automatic_attempts"`
	RetryBaseDelayMS             int                    `json:"retry_base_delay_ms"`
	RetryMaxDelayMS              int                    `json:"retry_max_delay_ms"`
	RetryThrottledDelayMS        int                    `json:"retry_throttled_delay_ms"`
	ProfileRepairSessionTTLMS    int                    `json:"profile_repair_session_ttl_ms"`
}

type ExecutorTargetConfig struct {
	ActorID    actor.ActorID `json:"actor_id"`
	Capability string        `json:"capability"`
}

func DefaultConfig() json.RawMessage {
	raw, _ := json.Marshal(defaultConfig())
	return raw
}

func parseConfig(raw json.RawMessage) (Config, error) {
	cfg := defaultConfig()
	if len(raw) != 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("recruiting config: %w", err)
		}
	}
	cfg.ExecutorID = actor.ActorID(strings.TrimSpace(string(cfg.ExecutorID)))
	cfg.DatabaseDSNEnv = strings.TrimSpace(cfg.DatabaseDSNEnv)
	cfg.DailyScheduleTimezone = strings.TrimSpace(cfg.DailyScheduleTimezone)
	cfg.DailyCutoffLocal = strings.TrimSpace(cfg.DailyCutoffLocal)
	seenExecutors := make(map[actor.ActorID]struct{}, len(cfg.Executors))
	for index := range cfg.Executors {
		cfg.Executors[index].ActorID = actor.ActorID(strings.TrimSpace(string(cfg.Executors[index].ActorID)))
		cfg.Executors[index].Capability = strings.TrimSpace(cfg.Executors[index].Capability)
		if !executioncontract.ValidToolTarget(string(cfg.Executors[index].ActorID)) || cfg.Executors[index].Capability == "" || len(cfg.Executors[index].Capability) > 128 ||
			strings.ContainsAny(cfg.Executors[index].Capability, "\r\n\t ") {
			return Config{}, fmt.Errorf("recruiting config: each executor requires actor_id and normalized capability")
		}
		if _, duplicate := seenExecutors[cfg.Executors[index].ActorID]; duplicate {
			return Config{}, fmt.Errorf("recruiting config: executor actor_id must be unique")
		}
		seenExecutors[cfg.Executors[index].ActorID] = struct{}{}
	}
	if len(cfg.Executors) > 10_000 {
		return Config{}, fmt.Errorf("recruiting config: at most 10000 executor instances are supported")
	}
	if cfg.ExecutorID == "" || cfg.DatabaseDSNEnv == "" || cfg.ReconcileIntervalMS < 100 || cfg.ReconcileIntervalMS > 3_600_000 {
		return Config{}, fmt.Errorf("recruiting config: executor_id, database_dsn_env, and reconcile_interval_ms in [100,3600000] are required")
	}
	if cfg.AttemptStaleAfterMS < 1_000 || cfg.AttemptStaleAfterMS > 86_400_000 || cfg.AttemptRecoveryLimit < 1 || cfg.AttemptRecoveryLimit > 500 {
		return Config{}, fmt.Errorf("recruiting config: attempt_stale_after_ms must be in [1000,86400000] and attempt_recovery_limit in [1,500]")
	}
	if cfg.DailyWorkMaterializeLimit < 1 || cfg.DailyWorkMaterializeLimit > 500 {
		return Config{}, fmt.Errorf("recruiting config: daily_work_materialize_limit must be in [1,500]")
	}
	if cfg.CompanyImportApplyLimit < 1 || cfg.CompanyImportApplyLimit > 500 {
		return Config{}, fmt.Errorf("recruiting config: company_import_apply_limit must be in [1,500]")
	}
	if cfg.BaselineMaterializeLimit < 1 || cfg.BaselineMaterializeLimit > 500 {
		return Config{}, fmt.Errorf("recruiting config: baseline_materialize_limit must be in [1,500]")
	}
	budget := cfg.executionBudgetPolicy()
	if budget.Version == 0 || budget.MaxActive < 1 || budget.MaxActive > 100_000 ||
		budget.MaxPerCapability < 1 || budget.MaxPerCapability > budget.MaxActive ||
		budget.MaxPerOrigin < 1 || budget.MaxPerOrigin > budget.MaxActive ||
		budget.MaxPerCompany < 1 || budget.MaxPerCompany > budget.MaxActive ||
		budget.MaxPerProfile < 1 || budget.MaxPerProfile > budget.MaxActive ||
		budget.PermitTTL < time.Second || budget.PermitTTL > 24*time.Hour {
		return Config{}, fmt.Errorf("recruiting config: invalid execution budget policy")
	}
	if err := cfg.executionFailurePolicy().Validate(); err != nil {
		return Config{}, fmt.Errorf("recruiting config: %w", err)
	}
	if cfg.ProfileRepairSessionTTLMS < 60_000 || cfg.ProfileRepairSessionTTLMS > 3_600_000 {
		return Config{}, fmt.Errorf("recruiting config: profile_repair_session_ttl_ms must be in [60000,3600000]")
	}
	if cfg.DailyScheduleEnabled {
		if _, err := time.LoadLocation(cfg.DailyScheduleTimezone); err != nil {
			return Config{}, fmt.Errorf("recruiting config: invalid daily_schedule_timezone: %w", err)
		}
		if _, err := time.Parse("15:04:05", cfg.DailyCutoffLocal); err != nil {
			return Config{}, fmt.Errorf("recruiting config: daily_cutoff_local must be HH:MM:SS")
		}
		if cfg.DailyWindowStartDelayMinutes < 0 || cfg.DailyWindowStartDelayMinutes > 1_440 ||
			cfg.DailyWindowDurationMinutes < 1 || cfg.DailyWindowDurationMinutes > 2_880 || cfg.DailySchedulePolicyVersion == 0 {
			return Config{}, fmt.Errorf("recruiting config: invalid daily window or policy version")
		}
	}
	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		ExecutorID: "recruiting-executor", DatabaseDSNEnv: "ATOLL_RECRUITING_MYSQL_DSN", ReconcileIntervalMS: 30_000,
		AttemptStaleAfterMS: 900_000, AttemptRecoveryLimit: 100,
		DailyScheduleEnabled: true, DailyScheduleTimezone: "UTC", DailyCutoffLocal: "00:00:00",
		DailyWindowStartDelayMinutes: 0, DailyWindowDurationMinutes: 480, DailySchedulePolicyVersion: 1,
		DailyWorkMaterializeLimit: 100,
		CompanyImportApplyLimit:   100,
		BaselineMaterializeLimit:  100,
		BudgetPolicyVersion:       1, BudgetMaxActive: 1_000, BudgetMaxPerCapability: 1_000,
		BudgetMaxPerOrigin: 8, BudgetMaxPerCompany: 50, BudgetMaxPerProfile: 1, BudgetPermitTTLMS: 900_000,
		RetryPolicyVersion: 1, RetryMaxAutomaticAttempts: 4, RetryBaseDelayMS: 30_000,
		RetryMaxDelayMS: 1_800_000, RetryThrottledDelayMS: 300_000,
		ProfileRepairSessionTTLMS: 600_000,
	}
}

func (c Config) executionBudgetPolicy() store.ExecutionBudgetPolicy {
	return store.ExecutionBudgetPolicy{Version: c.BudgetPolicyVersion, MaxActive: c.BudgetMaxActive,
		MaxPerCapability: c.BudgetMaxPerCapability, MaxPerOrigin: c.BudgetMaxPerOrigin,
		MaxPerCompany: c.BudgetMaxPerCompany, MaxPerProfile: c.BudgetMaxPerProfile,
		PermitTTL: time.Duration(c.BudgetPermitTTLMS) * time.Millisecond}
}

func (c Config) executionFailurePolicy() store.ExecutionFailurePolicy {
	return store.ExecutionFailurePolicy{Version: c.RetryPolicyVersion, MaxAutomaticAttempts: uint64(c.RetryMaxAutomaticAttempts),
		BaseDelay: time.Duration(c.RetryBaseDelayMS) * time.Millisecond, MaxDelay: time.Duration(c.RetryMaxDelayMS) * time.Millisecond,
		ThrottledDelay: time.Duration(c.RetryThrottledDelayMS) * time.Millisecond}
}

func (c Config) executionDispatchTargets() []store.ExecutionDispatchTarget {
	targets := make([]store.ExecutionDispatchTarget, 0, len(c.Executors))
	for _, executor := range c.Executors {
		targets = append(targets, store.ExecutionDispatchTarget{ActorID: string(executor.ActorID), Capability: executor.Capability})
	}
	return targets
}

func (c Config) executionDispatchTarget(capability, seed string) (store.ExecutionDispatchTarget, bool) {
	candidates := make([]store.ExecutionDispatchTarget, 0, len(c.Executors))
	for _, target := range c.executionDispatchTargets() {
		if target.Capability == capability {
			candidates = append(candidates, target)
		}
	}
	if len(candidates) == 0 {
		return store.ExecutionDispatchTarget{}, false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ActorID < candidates[j].ActorID })
	sum := sha256.Sum256([]byte(seed))
	return candidates[int(binary.BigEndian.Uint64(sum[:8])%uint64(len(candidates)))], true
}

const ConfigSchema = `{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "executor_id":{"type":"string","minLength":1},
    "executors":{"type":"array","maxItems":10000,"items":{"type":"object","additionalProperties":false,"required":["actor_id","capability"],"properties":{"actor_id":{"type":"string","minLength":1},"capability":{"type":"string","minLength":1,"maxLength":128}}}},
    "database_dsn_env":{"type":"string","minLength":1},
    "reconcile_interval_ms":{"type":"integer","minimum":100,"maximum":3600000},
    "attempt_stale_after_ms":{"type":"integer","minimum":1000,"maximum":86400000},
    "attempt_recovery_limit":{"type":"integer","minimum":1,"maximum":500},
    "daily_schedule_enabled":{"type":"boolean"},
    "daily_schedule_timezone":{"type":"string","minLength":1},
    "daily_cutoff_local":{"type":"string","pattern":"^[0-9]{2}:[0-9]{2}:[0-9]{2}$"},
    "daily_window_start_delay_minutes":{"type":"integer","minimum":0,"maximum":1440},
    "daily_window_duration_minutes":{"type":"integer","minimum":1,"maximum":2880},
    "daily_schedule_policy_version":{"type":"integer","minimum":1},
    "daily_work_materialize_limit":{"type":"integer","minimum":1,"maximum":500}
	,"company_import_apply_limit":{"type":"integer","minimum":1,"maximum":500}
	,"baseline_materialize_limit":{"type":"integer","minimum":1,"maximum":500}
	,"budget_policy_version":{"type":"integer","minimum":1}
	,"budget_max_active":{"type":"integer","minimum":1,"maximum":100000}
	,"budget_max_per_capability":{"type":"integer","minimum":1,"maximum":100000}
	,"budget_max_per_origin":{"type":"integer","minimum":1,"maximum":100000}
	,"budget_max_per_company":{"type":"integer","minimum":1,"maximum":100000}
	,"budget_max_per_profile":{"type":"integer","minimum":1,"maximum":100000}
	,"budget_permit_ttl_ms":{"type":"integer","minimum":1000,"maximum":86400000}
	,"retry_policy_version":{"type":"integer","minimum":1}
	,"retry_max_automatic_attempts":{"type":"integer","minimum":1,"maximum":100}
	,"retry_base_delay_ms":{"type":"integer","minimum":1000,"maximum":86400000}
	,"retry_max_delay_ms":{"type":"integer","minimum":1000,"maximum":604800000}
	,"retry_throttled_delay_ms":{"type":"integer","minimum":1000,"maximum":604800000}
	,"profile_repair_session_ttl_ms":{"type":"integer","minimum":60000,"maximum":3600000}
  }
}`
