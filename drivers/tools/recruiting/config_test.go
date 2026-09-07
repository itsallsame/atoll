package recruiting

import (
	"encoding/json"
	"testing"
)

func TestParseConfigDefaultsAndRejectsUnknownFields(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil || cfg.ExecutorID != "recruiting-executor" {
		t.Fatalf("default config = %+v, %v", cfg, err)
	}
	if _, err := parseConfig(json.RawMessage(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown config field was accepted")
	}
	if _, err := parseConfig(json.RawMessage(`{"executor_id":" "}`)); err == nil {
		t.Fatal("blank executor identity was accepted")
	}
}

func TestManifestExposesControlAndExecutorResultWords(t *testing.T) {
	words := manifest().Words
	for _, word := range []string{TypeProbeStart, TypeProbeSchedule, TypeProbeStatus, TypeExecutionResult} {
		if _, ok := words[word]; !ok {
			t.Fatalf("manifest does not expose %q", word)
		}
	}
}
