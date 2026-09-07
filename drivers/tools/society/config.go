package society

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/society/model"
)

const Class = "society"

type Config struct {
	Variant        string `json:"variant"`
	Seed           uint64 `json:"seed"`
	Days           int    `json:"days"`
	TickIntervalMS int    `json:"tick_interval_ms"`
}

func DefaultConfig() json.RawMessage {
	raw, _ := json.Marshal(Config{Variant: "A", Seed: model.DefaultSeed, Days: 10 * model.DaysPerYear, TickIntervalMS: 100})
	return raw
}

func parseConfig(raw json.RawMessage) (Config, error) {
	cfg := Config{Variant: "A", Seed: model.DefaultSeed, Days: 10 * model.DaysPerYear, TickIntervalMS: 100}
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("society config: %w", err)
		}
	}
	cfg.Variant = strings.ToUpper(strings.TrimSpace(cfg.Variant))
	if _, err := model.DefaultConfig(cfg.Variant); err != nil {
		return Config{}, err
	}
	if cfg.Seed == 0 {
		return Config{}, fmt.Errorf("society config: seed must be non-zero")
	}
	if cfg.Days < 1 {
		return Config{}, fmt.Errorf("society config: days must be positive")
	}
	if cfg.TickIntervalMS < 1 || cfg.TickIntervalMS > 60_000 {
		return Config{}, fmt.Errorf("society config: tick_interval_ms must be in [1,60000]")
	}
	return cfg, nil
}

func (c Config) modelConfig() (model.Config, error) {
	cfg, err := model.DefaultConfig(c.Variant)
	if err != nil {
		return model.Config{}, err
	}
	cfg.Seed, cfg.Days = c.Seed, c.Days
	return cfg, nil
}

const ConfigSchema = `{
  "type":"object",
  "additionalProperties":false,
  "properties":{
    "variant":{"type":"string","enum":["A","B","C","D"]},
    "seed":{"type":"integer","minimum":1},
    "days":{"type":"integer","minimum":1},
    "tick_interval_ms":{"type":"integer","minimum":1,"maximum":60000}
  }
}`
