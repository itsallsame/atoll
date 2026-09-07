package society

import (
	"encoding/json"
	"testing"

	"github.com/wanpengxie/atoll/platform/channelspec"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/registry"
)

func TestConfigDefaultsAndStrictValidation(t *testing.T) {
	cfg, err := parseConfig(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Variant != "A" || cfg.Seed == 0 || cfg.Days != 3650 || cfg.TickIntervalMS != 100 {
		t.Fatalf("defaults=%+v", cfg)
	}
	for _, raw := range []string{
		`{"variant":"E","seed":1,"days":1,"tick_interval_ms":1}`,
		`{"variant":"A","seed":0,"days":1,"tick_interval_ms":1}`,
		`{"variant":"A","seed":1,"days":0,"tick_interval_ms":1}`,
		`{"variant":"A","seed":1,"days":1,"tick_interval_ms":0}`,
		`{"variant":"A","seed":1,"days":1,"tick_interval_ms":1,"typo":true}`,
	} {
		if _, err := parseConfig(json.RawMessage(raw)); err == nil {
			t.Fatalf("parseConfig(%s)=ok", raw)
		}
	}
}

func TestSocietyClassIsRegisteredAsServerTool(t *testing.T) {
	if kind, ok := registry.ClassKind(Class); !ok || kind != actor.KindTool {
		t.Fatalf("kind=(%q,%v)", kind, ok)
	}
	if placement, ok := registry.ClassPlacement(Class); !ok || placement != channelspec.PlacementServer {
		t.Fatalf("placement=(%q,%v)", placement, ok)
	}
	if err := registry.ValidateConfig(Class, json.RawMessage(`{"variant":"D","seed":7,"days":40,"tick_interval_ms":5}`)); err != nil {
		t.Fatal(err)
	}
}

func TestConstructUsesSeatIdentityAndPublishesControlWords(t *testing.T) {
	decl, err := construct(registry.InstanceSpec{ID: "tool:town:1", Config: json.RawMessage(`{"variant":"B","seed":9,"days":10,"tick_interval_ms":2}`)}, registry.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if decl.ID != "tool:town:1" || decl.Kind != actor.KindTool {
		t.Fatalf("decl=%+v", decl)
	}
	if decl.Factory.Proc.New == nil {
		t.Fatal("society proc factory missing")
	}
	words := manifest().Words
	for _, word := range []string{TypeStatus, TypeStart, TypePause, TypeResume, TypeStep, TypeStop, TypeReset, TypeIntervene, TypeSnapshot, TypeHistory, TypeSpeed, TypeTrace, TypeRecent, TypeExport} {
		if _, ok := words[word]; !ok {
			t.Fatalf("manifest missing %s", word)
		}
	}
}
