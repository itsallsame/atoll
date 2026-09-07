package recruitingexecutor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const Class = "recruiting-executor"

type Config struct {
	Capability string `json:"capability"`
}

func DefaultConfig() json.RawMessage {
	raw, _ := json.Marshal(Config{Capability: "fixture"})
	return raw
}

func parseConfig(raw json.RawMessage) (Config, error) {
	cfg := Config{Capability: "fixture"}
	if len(raw) != 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("recruiting executor config: %w", err)
		}
	}
	cfg.Capability = strings.TrimSpace(cfg.Capability)
	if cfg.Capability == "" {
		return Config{}, fmt.Errorf("recruiting executor config: capability is required")
	}
	return cfg, nil
}

const ConfigSchema = `{
  "type":"object",
  "additionalProperties":false,
  "properties":{"capability":{"type":"string","minLength":1}}
}`
