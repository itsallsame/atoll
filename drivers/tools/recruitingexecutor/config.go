package recruitingexecutor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/protocol/actor"
)

const Class = "recruiting-executor"

type Config struct {
	Capability              string        `json:"capability"`
	ExecutionEnabled        bool          `json:"execution_enabled"`
	ControlActorID          actor.ActorID `json:"control_actor_id,omitempty"`
	ControlWaitMS           int           `json:"control_wait_ms,omitempty"`
	ArtifactDeviceName      string        `json:"artifact_device_name,omitempty"`
	ArtifactChannelName     string        `json:"artifact_channel_name,omitempty"`
	ArtifactDirectory       string        `json:"artifact_directory,omitempty"`
	ArtifactAccessScope     string        `json:"artifact_access_scope,omitempty"`
	ArtifactRetention       string        `json:"artifact_retention,omitempty"`
	ArtifactRedaction       string        `json:"artifact_redaction,omitempty"`
	ArtifactMaxBytes        int64         `json:"artifact_max_bytes,omitempty"`
	TermsPolicyVersion      uint64        `json:"terms_policy_version,omitempty"`
	TermsReviewedAt         string        `json:"terms_reviewed_at,omitempty"`
	HTTPMaxConcurrency      int           `json:"http_max_concurrency,omitempty"`
	HTTPMinOriginIntervalMS int           `json:"http_min_origin_interval_ms,omitempty"`
	HTTPCircuitThreshold    int           `json:"http_circuit_threshold,omitempty"`
	HTTPCircuitCooldownMS   int           `json:"http_circuit_cooldown_ms,omitempty"`
	RobotsTimeoutMS         int           `json:"robots_timeout_ms,omitempty"`
	RobotsMaxBytes          int64         `json:"robots_max_bytes,omitempty"`
	RobotsCacheTTLMS        int           `json:"robots_cache_ttl_ms,omitempty"`
	BatchMaxBytes           int64         `json:"batch_max_bytes,omitempty"`
	BatchChunkSize          int           `json:"batch_chunk_size,omitempty"`
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
			return Config{}, fmt.Errorf("recruiting executor config: %w", err)
		}
	}
	cfg.Capability = strings.TrimSpace(cfg.Capability)
	cfg.ControlActorID = actor.ActorID(strings.TrimSpace(string(cfg.ControlActorID)))
	cfg.ArtifactDeviceName = strings.TrimSpace(cfg.ArtifactDeviceName)
	cfg.ArtifactChannelName = strings.TrimSpace(cfg.ArtifactChannelName)
	cfg.ArtifactDirectory = strings.Trim(strings.TrimSpace(cfg.ArtifactDirectory), "/")
	cfg.ArtifactAccessScope = strings.TrimSpace(cfg.ArtifactAccessScope)
	cfg.ArtifactRetention = strings.TrimSpace(cfg.ArtifactRetention)
	cfg.ArtifactRedaction = strings.TrimSpace(cfg.ArtifactRedaction)
	cfg.TermsReviewedAt = strings.TrimSpace(cfg.TermsReviewedAt)
	if cfg.Capability == "" {
		return Config{}, fmt.Errorf("recruiting executor config: capability is required")
	}
	if cfg.ExecutionEnabled {
		if (cfg.Capability != "http.fetch" && cfg.Capability != "company.import") || !executioncontract.ValidToolTarget(string(cfg.ControlActorID)) || cfg.ControlWaitMS < 100 || cfg.ControlWaitMS > 300_000 ||
			cfg.ArtifactDeviceName == "" || cfg.ArtifactChannelName == "" || cfg.ArtifactDirectory == "" ||
			cfg.ArtifactAccessScope == "" || cfg.ArtifactRetention == "" || cfg.ArtifactMaxBytes < 1 || cfg.ArtifactMaxBytes > 20<<20 ||
			(cfg.ArtifactRedaction != "raw" && cfg.ArtifactRedaction != "redacted") {
			return Config{}, fmt.Errorf("recruiting executor config: enabled execution requires a supported capability, control, and Artifact policy")
		}
		if cfg.Capability == "http.fetch" {
			if cfg.TermsPolicyVersion == 0 {
				return Config{}, fmt.Errorf("recruiting executor config: HTTP execution requires terms policy")
			}
			if _, err := time.Parse(time.RFC3339, cfg.TermsReviewedAt); err != nil {
				return Config{}, fmt.Errorf("recruiting executor config: terms_reviewed_at must be RFC3339")
			}
			if cfg.HTTPMaxConcurrency < 1 || cfg.HTTPMaxConcurrency > 1000 || cfg.HTTPMinOriginIntervalMS < 0 || cfg.HTTPMinOriginIntervalMS > 3_600_000 ||
				cfg.HTTPCircuitThreshold < 1 || cfg.HTTPCircuitThreshold > 100 || cfg.HTTPCircuitCooldownMS < 1_000 || cfg.HTTPCircuitCooldownMS > 86_400_000 ||
				cfg.RobotsTimeoutMS < 100 || cfg.RobotsTimeoutMS > 30_000 || cfg.RobotsMaxBytes < 1 || cfg.RobotsMaxBytes > 1<<20 ||
				cfg.RobotsCacheTTLMS < 60_000 || cfg.RobotsCacheTTLMS > 86_400_000 {
				return Config{}, fmt.Errorf("recruiting executor config: invalid HTTP or robots policy")
			}
		}
		if cfg.Capability == "company.import" && (cfg.BatchMaxBytes < 1 || cfg.BatchMaxBytes > 100<<20 || cfg.BatchChunkSize < 1 || cfg.BatchChunkSize > 500) {
			return Config{}, fmt.Errorf("recruiting executor config: invalid company import byte or chunk limit")
		}
	}
	return cfg, nil
}

func defaultConfig() Config {
	return Config{Capability: "fixture", ControlWaitMS: 30_000, ArtifactMaxBytes: 2 << 20,
		HTTPMaxConcurrency: 4, HTTPMinOriginIntervalMS: 1_000, HTTPCircuitThreshold: 3, HTTPCircuitCooldownMS: 300_000,
		RobotsTimeoutMS: 10_000, RobotsMaxBytes: 1 << 20, RobotsCacheTTLMS: 3_600_000,
		BatchMaxBytes: 20 << 20, BatchChunkSize: 250}
}

const ConfigSchema = `{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "capability":{"type":"string","minLength":1},
    "execution_enabled":{"type":"boolean"},
    "control_actor_id":{"type":"string","minLength":1},
    "control_wait_ms":{"type":"integer","minimum":100,"maximum":300000},
    "artifact_device_name":{"type":"string","minLength":1},
    "artifact_channel_name":{"type":"string","minLength":1},
    "artifact_directory":{"type":"string","minLength":1},
    "artifact_access_scope":{"type":"string","minLength":1},
    "artifact_retention":{"type":"string","minLength":1},
    "artifact_redaction":{"type":"string","enum":["raw","redacted"]},
    "artifact_max_bytes":{"type":"integer","minimum":1,"maximum":20971520},
    "terms_policy_version":{"type":"integer","minimum":1},
    "terms_reviewed_at":{"type":"string","minLength":1},
    "http_max_concurrency":{"type":"integer","minimum":1,"maximum":1000},
    "http_min_origin_interval_ms":{"type":"integer","minimum":0,"maximum":3600000},
    "http_circuit_threshold":{"type":"integer","minimum":1,"maximum":100},
    "http_circuit_cooldown_ms":{"type":"integer","minimum":1000,"maximum":86400000},
    "robots_timeout_ms":{"type":"integer","minimum":100,"maximum":30000},
    "robots_max_bytes":{"type":"integer","minimum":1,"maximum":1048576},
    "robots_cache_ttl_ms":{"type":"integer","minimum":60000,"maximum":86400000},
    "batch_max_bytes":{"type":"integer","minimum":1,"maximum":104857600},
    "batch_chunk_size":{"type":"integer","minimum":1,"maximum":500}
  }
}`
