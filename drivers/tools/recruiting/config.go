package recruiting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/protocol/actor"
)

const Class = "recruiting"

const DefaultActorID actor.ActorID = "recruiting"

type Config struct {
	ExecutorID                   actor.ActorID `json:"executor_id"`
	DatabaseDSNEnv               string        `json:"database_dsn_env"`
	ReconcileIntervalMS          int           `json:"reconcile_interval_ms"`
	AttemptStaleAfterMS          int           `json:"attempt_stale_after_ms"`
	AttemptRecoveryLimit         int           `json:"attempt_recovery_limit"`
	DailyScheduleEnabled         bool          `json:"daily_schedule_enabled"`
	DailyScheduleTimezone        string        `json:"daily_schedule_timezone"`
	DailyCutoffLocal             string        `json:"daily_cutoff_local"`
	DailyWindowStartDelayMinutes int           `json:"daily_window_start_delay_minutes"`
	DailyWindowDurationMinutes   int           `json:"daily_window_duration_minutes"`
	DailySchedulePolicyVersion   uint64        `json:"daily_schedule_policy_version"`
	DailyWorkMaterializeLimit    int           `json:"daily_work_materialize_limit"`
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
	if cfg.ExecutorID == "" || cfg.DatabaseDSNEnv == "" || cfg.ReconcileIntervalMS < 100 || cfg.ReconcileIntervalMS > 3_600_000 {
		return Config{}, fmt.Errorf("recruiting config: executor_id, database_dsn_env, and reconcile_interval_ms in [100,3600000] are required")
	}
	if cfg.AttemptStaleAfterMS < 1_000 || cfg.AttemptStaleAfterMS > 86_400_000 || cfg.AttemptRecoveryLimit < 1 || cfg.AttemptRecoveryLimit > 500 {
		return Config{}, fmt.Errorf("recruiting config: attempt_stale_after_ms must be in [1000,86400000] and attempt_recovery_limit in [1,500]")
	}
	if cfg.DailyWorkMaterializeLimit < 1 || cfg.DailyWorkMaterializeLimit > 500 {
		return Config{}, fmt.Errorf("recruiting config: daily_work_materialize_limit must be in [1,500]")
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
	}
}

const ConfigSchema = `{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "executor_id":{"type":"string","minLength":1},
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
  }
}`
