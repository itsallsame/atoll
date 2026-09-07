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
	ExecutorID actor.ActorID `json:"executor_id"`
}

func DefaultConfig() json.RawMessage {
	raw, _ := json.Marshal(Config{ExecutorID: "recruiting-executor"})
	return raw
}

func parseConfig(raw json.RawMessage) (Config, error) {
	cfg := Config{ExecutorID: "recruiting-executor"}
	if len(raw) != 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("recruiting config: %w", err)
		}
	}
	cfg.ExecutorID = actor.ActorID(strings.TrimSpace(string(cfg.ExecutorID)))
	if cfg.ExecutorID == "" {
		return Config{}, fmt.Errorf("recruiting config: executor_id is required")
	}
	return cfg, nil
}

const ConfigSchema = `{
  "type":"object",
  "additionalProperties":false,
  "properties":{"executor_id":{"type":"string","minLength":1}}
}`
