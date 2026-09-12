package recruitingexecutor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserbroker"
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
		"terms_policy_version":3,"terms_reviewed_at":"2026-09-08T00:00:00Z","execution_batch_size":32
	}`)
	cfg, err := parseConfig(raw)
	if err != nil || !cfg.ExecutionEnabled || cfg.ControlWaitMS != 30_000 || cfg.ArtifactMaxBytes != 2<<20 ||
		cfg.HTTPMaxConcurrency != 4 || cfg.ExecutionBatchSize != 32 || cfg.RobotsCacheTTLMS != 3_600_000 {
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
		json.RawMessage(`{"capability":"artifact.recompute","execution_enabled":true,"control_actor_id":"tool:control","artifact_device_name":"worker-a","artifact_channel_name":"recruiting","artifact_directory":"artifacts","artifact_access_scope":"operators","artifact_retention":"30d","artifact_redaction":"raw","execution_batch_size":2}`),
	} {
		if _, err := parseConfig(invalid); err == nil {
			t.Fatalf("unsafe production config was accepted: %s", invalid)
		}
	}
}

func TestParseConfigEnablesCompanyImportInSameExecutorClassWithoutHTTPPolicy(t *testing.T) {
	raw := json.RawMessage(`{
		"capability":"company.import","execution_enabled":true,"control_actor_id":"tool:control",
		"artifact_device_name":"worker-a","artifact_channel_name":"recruiting","artifact_directory":"artifacts",
		"artifact_access_scope":"operators","artifact_retention":"30d","artifact_redaction":"redacted",
		"batch_max_bytes":1048576,"batch_chunk_size":200
	}`)
	cfg, err := parseConfig(raw)
	if err != nil || cfg.Capability != "company.import" || cfg.BatchChunkSize != 200 || cfg.TermsPolicyVersion != 0 {
		t.Fatalf("company import config = %+v err=%v", cfg, err)
	}
	runtime, err := newProductionRuntime(cfg)
	if err != nil || runtime.driver != nil || runtime.batchOptions.ChunkSize != 200 {
		t.Fatalf("company import runtime = %+v err=%v", runtime, err)
	}
}

func TestParseConfigEnablesArtifactRecomputeWithoutHTTPPolicy(t *testing.T) {
	raw := json.RawMessage(`{
		"capability":"artifact.recompute","execution_enabled":true,"control_actor_id":"tool:control",
		"artifact_device_name":"worker-a","artifact_channel_name":"recruiting","artifact_directory":"artifacts",
		"artifact_access_scope":"operators","artifact_retention":"30d","artifact_redaction":"redacted"
	}`)
	cfg, err := parseConfig(raw)
	if err != nil || cfg.Capability != "artifact.recompute" || cfg.TermsPolicyVersion != 0 {
		t.Fatalf("artifact recompute config = %+v err=%v", cfg, err)
	}
	runtime, err := newProductionRuntime(cfg)
	if err != nil || runtime.driver != nil || runtime.broker != nil {
		t.Fatalf("artifact recompute runtime = %+v err=%v", runtime, err)
	}
}

func TestParseConfigEnablesProfileBrokerInSameExecutorClass(t *testing.T) {
	raw := json.RawMessage(`{
		"capability":"browser.profile.repair","execution_enabled":true,"control_actor_id":"tool:control",
		"artifact_device_name":"worker-a","artifact_channel_name":"recruiting","artifact_directory":"artifacts",
		"artifact_access_scope":"operators","artifact_retention":"30d","artifact_redaction":"redacted",
		"browser_broker_url":"http://127.0.0.1:19090","browser_broker_token_file":"/run/user/1000/atoll-broker.token"
	}`)
	cfg, err := parseConfig(raw)
	if err != nil || cfg.Capability != "browser.profile.repair" || cfg.BrowserBrokerTimeoutMS != 900_000 {
		t.Fatalf("browser Profile config=%+v err=%v", cfg, err)
	}
	for _, invalid := range []string{"https://127.0.0.1:19090", "http://localhost:19090", "http://10.0.0.2:19090", "http://127.0.0.1:19090/path"} {
		candidate := json.RawMessage(`{
			"capability":"browser.profile.repair","execution_enabled":true,"control_actor_id":"tool:control",
			"artifact_device_name":"worker-a","artifact_channel_name":"recruiting","artifact_directory":"artifacts",
			"artifact_access_scope":"operators","artifact_retention":"30d","artifact_redaction":"redacted",
			"browser_broker_url":"` + invalid + `","browser_broker_token_file":"/tmp/token"
		}`)
		if _, err := parseConfig(candidate); err == nil {
			t.Fatalf("unsafe browser broker URL was accepted: %s", invalid)
		}
	}
}

func TestParseConfigEnablesPublicBrowserInSameExecutorClass(t *testing.T) {
	raw := json.RawMessage(`{
		"capability":"browser.public","execution_enabled":true,"control_actor_id":"tool:control",
		"artifact_device_name":"worker-a","artifact_channel_name":"recruiting","artifact_directory":"artifacts",
		"artifact_access_scope":"operators","artifact_retention":"30d","artifact_redaction":"redacted",
		"terms_policy_version":3,"terms_reviewed_at":"2026-09-08T00:00:00Z",
		"browser_chrome_path":"/bin/sh"
	}`)
	cfg, err := parseConfig(raw)
	if err != nil || cfg.Capability != "browser.public" || cfg.BrowserChromePath != "/bin/sh" {
		t.Fatalf("public browser config=%+v err=%v", cfg, err)
	}
	runtime, err := newProductionRuntime(cfg)
	if err != nil || runtime.driver == nil || runtime.broker != nil {
		t.Fatalf("public browser runtime=%+v err=%v", runtime, err)
	}
	withoutChrome := json.RawMessage(`{
		"capability":"browser.public","execution_enabled":true,"control_actor_id":"tool:control",
		"artifact_device_name":"worker-a","artifact_channel_name":"recruiting","artifact_directory":"artifacts",
		"artifact_access_scope":"operators","artifact_retention":"30d","artifact_redaction":"redacted",
		"terms_policy_version":3,"terms_reviewed_at":"2026-09-08T00:00:00Z"
	}`)
	if _, err := parseConfig(withoutChrome); err == nil {
		t.Fatal("public browser config without a Chrome executable was accepted")
	}
}

func TestParseConfigEnablesDailyProfileBrowserOnAuthorizedDevice(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, "profile")
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "profiles.json")
	rawRegistry, _ := json.Marshal(map[string]any{"version": browserbroker.ProfileRegistryVersion,
		"profiles": []map[string]any{{"profile_id": "profile-1", "profile_version": 1,
			"security_domain": "jobs.example.test", "user_data_dir": profileDir}}})
	if err := os.WriteFile(registry, rawRegistry, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"capability": "browser.recipe", "execution_enabled": true, "control_actor_id": "tool:control",
		"artifact_device_name": "worker-a", "artifact_channel_name": "recruiting", "artifact_directory": "artifacts",
		"artifact_access_scope": "operators", "artifact_retention": "30d", "artifact_redaction": "redacted",
		"terms_policy_version": 3, "terms_reviewed_at": "2026-09-11T00:00:00Z",
		"browser_chrome_path": "/bin/sh", "browser_profile_registry": registry,
	})
	cfg, err := parseConfig(raw)
	if err != nil || cfg.Capability != "browser.recipe" || cfg.BrowserProfileRegistry != registry {
		t.Fatalf("daily Profile Browser config=%+v err=%v", cfg, err)
	}
	runtime, err := newProductionRuntime(cfg)
	if err != nil || runtime.driver == nil || runtime.broker != nil {
		t.Fatalf("daily Profile Browser runtime=%+v err=%v", runtime, err)
	}
	var unsafe map[string]any
	_ = json.Unmarshal(raw, &unsafe)
	unsafe["artifact_redaction"] = "raw"
	unsafeRaw, _ := json.Marshal(unsafe)
	if _, err := parseConfig(unsafeRaw); err == nil {
		t.Fatal("daily Profile Browser accepted raw Artifact policy")
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

func TestWakeOfferCommandIsStablePerDeliveryAndChangesAcrossRedeliveryOrRestart(t *testing.T) {
	first := wakeOfferCommandID("boot-a", "dispatch-1", "delivery-1")
	if first == "" || first != wakeOfferCommandID("boot-a", "dispatch-1", "delivery-1") ||
		first == wakeOfferCommandID("boot-b", "dispatch-1", "delivery-1") ||
		first == wakeOfferCommandID("boot-a", "dispatch-2", "delivery-1") ||
		first == wakeOfferCommandID("boot-a", "dispatch-1", "delivery-2") {
		t.Fatalf("wake offer identity is not correctly scoped: %q", first)
	}
}
