package recruiting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wanpengxie/atoll/protocol/actor"
)

const Class = "recruiting"

const DefaultActorID actor.ActorID = "recruiting"

type Config struct {
	ExecutorID          actor.ActorID `json:"executor_id"`
	DatabaseDSNEnv      string        `json:"database_dsn_env"`
	ReconcileIntervalMS int           `json:"reconcile_interval_ms"`
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
	if cfg.ExecutorID == "" || cfg.DatabaseDSNEnv == "" || cfg.ReconcileIntervalMS < 100 || cfg.ReconcileIntervalMS > 3_600_000 {
		return Config{}, fmt.Errorf("recruiting config: executor_id, database_dsn_env, and reconcile_interval_ms in [100,3600000] are required")
	}
	return cfg, nil
}

func defaultConfig() Config {
	return Config{ExecutorID: "recruiting-executor", DatabaseDSNEnv: "ATOLL_RECRUITING_MYSQL_DSN", ReconcileIntervalMS: 30_000}
}

const ConfigSchema = `{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "executor_id":{"type":"string","minLength":1},
    "database_dsn_env":{"type":"string","minLength":1},
    "reconcile_interval_ms":{"type":"integer","minimum":100,"maximum":3600000}
  }
}`
