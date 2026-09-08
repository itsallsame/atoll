package recruitingexecutor

import (
	"encoding/json"
	"testing"
)

func TestParseConfigSupportsManyCapabilityInstances(t *testing.T) {
	first, err := parseConfig(json.RawMessage(`{"capability":"http.fetch"}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := parseConfig(json.RawMessage(`{"capability":"browser.recipe"}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.Capability == second.Capability {
		t.Fatalf("distinct executor capabilities collapsed: %+v %+v", first, second)
	}
	if _, err := parseConfig(json.RawMessage(`{"capability":"","worker_type":"browser"}`)); err == nil {
		t.Fatal("blank capability or role-shaped unknown field was accepted")
	}
}

func TestParseConfigRequiresExplicitProductionSafetyInputs(t *testing.T) {
	raw := json.RawMessage(`{
		"capability":"http.fetch","execution_enabled":true,"control_actor_id":"tool:control",
		"artifact_device_name":"worker-a","artifact_channel_name":"recruiting","artifact_directory":"artifacts",
		"artifact_access_scope":"operators","artifact_retention":"30d","artifact_redaction":"raw",
		"terms_policy_version":3,"terms_reviewed_at":"2026-09-08T00:00:00Z"
	}`)
	cfg, err := parseConfig(raw)
	if err != nil || !cfg.ExecutionEnabled || cfg.ControlWaitMS != 30_000 || cfg.ArtifactMaxBytes != 2<<20 ||
		cfg.HTTPMaxConcurrency != 4 || cfg.RobotsCacheTTLMS != 3_600_000 {
		t.Fatalf("production config = %+v err=%v", cfg, err)
	}
	if _, err := newProductionRuntime(cfg); err != nil {
		t.Fatalf("production runtime construction = %v", err)
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"capability":"http.fetch","execution_enabled":true}`),
		json.RawMessage(`{"capability":"http.fetch","execution_enabled":true,"control_actor_id":"control"}`),
		json.RawMessage(`{"capability":"browser.recipe","execution_enabled":true,"control_actor_id":"tool:control"}`),
		json.RawMessage(`{"capability":"http.fetch","execution_enabled":true,"control_actor_id":"tool:control","artifact_device_name":"worker-a","artifact_channel_name":"recruiting","artifact_directory":"artifacts","artifact_access_scope":"operators","artifact_retention":"30d","artifact_redaction":"unknown","terms_policy_version":3,"terms_reviewed_at":"2026-09-08T00:00:00Z"}`),
	} {
		if _, err := parseConfig(invalid); err == nil {
			t.Fatalf("unsafe production config was accepted: %s", invalid)
		}
	}
}

func TestManifestHasOneExecutorClass(t *testing.T) {
	m := manifest()
	if m.Class != Class {
		t.Fatalf("manifest class=%q want %q", m.Class, Class)
	}
	for _, word := range []string{TypeProbe, TypeWake} {
		if _, ok := m.Words[word]; !ok {
			t.Fatalf("manifest lacks %q", word)
		}
	}
}

func TestWakeOfferCommandIsStableWithinIncarnationAndChangesAcrossRestart(t *testing.T) {
	first := wakeOfferCommandID("boot-a", "wake-1")
	if first == "" || first != wakeOfferCommandID("boot-a", "wake-1") || first == wakeOfferCommandID("boot-b", "wake-1") ||
		first == wakeOfferCommandID("boot-a", "wake-2") {
		t.Fatalf("wake offer identity is not correctly scoped: %q", first)
	}
}
