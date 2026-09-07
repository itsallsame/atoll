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

func TestManifestHasOneExecutorClass(t *testing.T) {
	m := manifest()
	if m.Class != Class {
		t.Fatalf("manifest class=%q want %q", m.Class, Class)
	}
	if _, ok := m.Words[TypeProbe]; !ok {
		t.Fatalf("manifest lacks %q", TypeProbe)
	}
}
