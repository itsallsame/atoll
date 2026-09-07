package society

import (
	"encoding/json"

	"github.com/wanpengxie/atoll/lib/introspect"
	"github.com/wanpengxie/atoll/platform"
	"github.com/wanpengxie/atoll/platform/channelspec"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/registry"
)

func init() {
	registry.Register(Class, registry.ClassDecl{
		Kind: actor.KindTool, Placement: channelspec.PlacementServer,
		Manifest: manifest(), New: construct, DefaultConfig: DefaultConfig,
		ValidateConfig: func(raw json.RawMessage) error { _, err := parseConfig(raw); return err },
		ConfigSchema:   json.RawMessage(ConfigSchema),
	})
}

func construct(spec registry.InstanceSpec, _ registry.Deps) (platform.ActorDecl, error) {
	cfg, err := parseConfig(spec.Config)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	id := spec.ID
	if id == "" {
		id = actor.ActorID(Class)
	}
	return platform.ActorDecl{ID: id, Kind: actor.KindTool, Factory: platform.ActorFactory{Proc: Def(cfg)}}, nil
}

func manifest() introspect.Manifest {
	words := map[string]introspect.WordSpec{}
	for _, word := range []string{TypeStatus, TypeStart, TypePause, TypeResume, TypeStep, TypeStop, TypeReset, TypeIntervene, TypeSnapshot, TypeHistory, TypeSpeed, TypeTrace, TypePopulation, TypeNetwork, TypeInspect, TypeRecent, TypeExport} {
		words[word] = introspect.WordSpec{Description: "control or inspect the deterministic digital-society experiment"}
	}
	return introspect.Manifest{Class: Class, Interfaces: []string{"actor", "society-experiment"}, Words: words}
}
