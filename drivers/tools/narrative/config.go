package narrative

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const ConfigSchema = `{"type":"object","additionalProperties":false,"required":["bundle"],"properties":{"bundle":{"type":"string"},"character":{"type":"string"},"start_day":{"type":"integer","minimum":1},"channel_prefix":{"type":"string","pattern":"^[a-z0-9-]+$"}}}`

type Config struct {
	Bundle        string `json:"bundle"`
	Character     string `json:"character,omitempty"`
	StartDay      int    `json:"start_day,omitempty"`
	ChannelPrefix string `json:"channel_prefix,omitempty"`
}

func parseConfig(raw json.RawMessage, character bool) (Config, error) {
	if len(raw) == 0 {
		return Config{}, errors.New("narrative: config required")
	}
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("narrative config: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("narrative config: trailing JSON value")
	}
	cfg.Bundle = filepath.Clean(strings.TrimSpace(cfg.Bundle))
	cfg.Character = strings.TrimSpace(cfg.Character)
	if cfg.StartDay == 0 {
		cfg.StartDay = 2
	}
	if cfg.StartDay < 1 {
		return Config{}, errors.New("narrative config: start_day must be positive")
	}
	cfg.ChannelPrefix = strings.TrimSpace(cfg.ChannelPrefix)
	if cfg.ChannelPrefix == "" {
		cfg.ChannelPrefix = "narrative"
	}
	for _, r := range cfg.ChannelPrefix {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return Config{}, errors.New("narrative config: channel_prefix must contain only lowercase a-z, 0-9 and '-'")
		}
	}
	if cfg.Bundle == "." || cfg.Bundle == "" {
		return Config{}, errors.New("narrative config: bundle is required")
	}
	if character && cfg.Character == "" {
		return Config{}, errors.New("narrative config: character is required")
	}
	if !character && cfg.Character != "" {
		return Config{}, errors.New("narrative config: world actor does not take character")
	}
	return cfg, nil
}
