package narrative

import (
	"encoding/json"
	"errors"

	"github.com/wanpengxie/atoll/lib/introspect"
	"github.com/wanpengxie/atoll/platform"
	"github.com/wanpengxie/atoll/platform/channelspec"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/registry"
)

const (
	WorldClass       = "narrative-world"
	CharacterClass   = "narrative-character"
	EnvironmentClass = "narrative-environment"
	ProjectorClass   = "narrative-projector"
	PlannerClass     = "narrative-planner"
	WriterClass      = "narrative-writer"
	CriticClass      = "narrative-critic"
)

func init() {
	registry.Register(WorldClass, registry.ClassDecl{
		Kind: actor.KindTool, Placement: channelspec.PlacementServer,
		Manifest: worldManifest(), New: constructWorld,
		ValidateConfig: func(raw json.RawMessage) error { _, err := parseConfig(raw, false); return err },
		ConfigSchema:   json.RawMessage(ConfigSchema),
	})
	registry.Register(CharacterClass, registry.ClassDecl{
		Kind: actor.KindAgent, Placement: channelspec.PlacementServer,
		Manifest: characterManifest(), New: constructCharacter,
		ValidateConfig: func(raw json.RawMessage) error { _, err := parseConfig(raw, true); return err },
		ConfigSchema:   json.RawMessage(ConfigSchema),
	})
	registry.Register(EnvironmentClass, registry.ClassDecl{
		Kind: actor.KindTool, Placement: channelspec.PlacementServer,
		Manifest: environmentManifest(), New: constructEnvironment,
		ValidateConfig: func(raw json.RawMessage) error { _, err := parseConfig(raw, false); return err },
		ConfigSchema:   json.RawMessage(ConfigSchema),
	})
	registry.Register(ProjectorClass, registry.ClassDecl{
		Kind: actor.KindTool, Placement: channelspec.PlacementServer,
		Manifest: projectorManifest(), New: constructProjector,
		ValidateConfig: func(raw json.RawMessage) error { _, err := parseConfig(raw, false); return err },
		ConfigSchema:   json.RawMessage(ConfigSchema),
	})
	for _, declaration := range []struct {
		class     string
		kind      actor.Kind
		manifest  introspect.Manifest
		construct registry.Constructor
	}{
		{PlannerClass, actor.KindTool, plannerManifest(), constructPlanner},
		{WriterClass, actor.KindAgent, writerManifest(), constructWriter},
		{CriticClass, actor.KindAgent, criticManifest(), constructCritic},
	} {
		registry.Register(declaration.class, registry.ClassDecl{Kind: declaration.kind, Placement: channelspec.PlacementServer,
			Manifest: declaration.manifest, New: declaration.construct,
			ValidateConfig: func(raw json.RawMessage) error { _, err := parseConfig(raw, false); return err }, ConfigSchema: json.RawMessage(ConfigSchema)})
	}
}

func constructPlanner(spec registry.InstanceSpec, _ registry.Deps) (platform.ActorDecl, error) {
	if _, err := parseConfig(spec.Config, false); err != nil {
		return platform.ActorDecl{}, err
	}
	return platform.ActorDecl{ID: spec.ID, Kind: actor.KindTool, Factory: platform.ActorFactory{Proc: plannerDef()}}, nil
}
func constructWriter(spec registry.InstanceSpec, _ registry.Deps) (platform.ActorDecl, error) {
	cfg, err := parseConfig(spec.Config, false)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	generator, err := newProseGeneratorFromEnvironment(cfg.Bundle)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	return platform.ActorDecl{ID: spec.ID, Kind: actor.KindAgent, Factory: platform.ActorFactory{Proc: writerDef(generator)}}, nil
}
func constructCritic(spec registry.InstanceSpec, _ registry.Deps) (platform.ActorDecl, error) {
	cfg, err := parseConfig(spec.Config, false)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	generator, err := newProseGeneratorFromEnvironment(cfg.Bundle)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	var critic proseCritic
	if generator != nil {
		critic = generator.(proseCritic)
	}
	return platform.ActorDecl{ID: spec.ID, Kind: actor.KindAgent, Factory: platform.ActorFactory{Proc: criticDef(critic)}}, nil
}

func constructEnvironment(spec registry.InstanceSpec, _ registry.Deps) (platform.ActorDecl, error) {
	cfg, err := parseConfig(spec.Config, false)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	simulation, err := loadSimulation(cfg.Bundle)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	return platform.ActorDecl{ID: spec.ID, Kind: actor.KindTool, Factory: platform.ActorFactory{Proc: environmentDef(simulation)}}, nil
}

func constructProjector(spec registry.InstanceSpec, _ registry.Deps) (platform.ActorDecl, error) {
	if _, err := parseConfig(spec.Config, false); err != nil {
		return platform.ActorDecl{}, err
	}
	return platform.ActorDecl{ID: spec.ID, Kind: actor.KindTool, Factory: platform.ActorFactory{Proc: projectorDef()}}, nil
}

func constructWorld(spec registry.InstanceSpec, _ registry.Deps) (platform.ActorDecl, error) {
	if spec.ID == "" {
		return platform.ActorDecl{}, errors.New("narrative-world: explicit instance id required")
	}
	cfg, err := parseConfig(spec.Config, false)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	simulation, err := loadSimulation(cfg.Bundle)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	initial, err := loadInitialWorld(cfg.Bundle, cfg.StartDay)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	return platform.ActorDecl{ID: spec.ID, Kind: actor.KindTool, Factory: platform.ActorFactory{Proc: worldDef(simulation, initial, cfg.ChannelPrefix)}}, nil
}

func constructCharacter(spec registry.InstanceSpec, _ registry.Deps) (platform.ActorDecl, error) {
	if spec.ID == "" {
		return platform.ActorDecl{}, errors.New("narrative-character: explicit instance id required")
	}
	cfg, err := parseConfig(spec.Config, true)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	simulation, err := loadSimulation(cfg.Bundle)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	initial, err := loadInitialBelief(cfg.Bundle, cfg.Character, cfg.StartDay)
	if err != nil {
		return platform.ActorDecl{}, err
	}
	return platform.ActorDecl{ID: spec.ID, Kind: actor.KindAgent, Factory: platform.ActorFactory{Proc: characterDef(cfg.Character, simulation, initial)}}, nil
}

func worldManifest() introspect.Manifest {
	return introspect.Manifest{Class: WorldClass, Interfaces: []string{"actor", "narrative-world"}, Words: map[string]introspect.WordSpec{
		TypeEvolve:             {Description: "Advance one narrative day through Atoll actor calls and ledger events.", ErrorCodes: []string{"bad_payload", "evolution_failed"}},
		TypeSchedule:           {Description: "Schedule a durable Atoll timer that advances one narrative day.", ErrorCodes: []string{"bad_payload", "schedule_failed"}},
		TypeStatus:             {Description: "Read the world's durable evolution position.", ErrorCodes: []string{"bad_payload"}},
		TypeInject:             {Description: "Commit an audited external stimulus, update the world, and notify only its observers.", ErrorCodes: []string{"bad_payload", "injection_failed"}},
		TypeAutoStart:          {Description: "Start a bounded durable evolution loop that stops at a target day or after consecutive empty days.", ErrorCodes: []string{"bad_payload", "conflict_exists", "schedule_failed", "state_failed"}},
		TypeAutoStop:           {Description: "Stop the durable evolution loop without deleting world state.", ErrorCodes: []string{"bad_payload", "state_failed"}},
		TypeProvenanceBackfill: {Description: "Backfill missing state-to-event provenance without changing world values.", ErrorCodes: []string{"bad_payload", "state_failed"}},
	}}
}

func characterManifest() introspect.Manifest {
	return introspect.Manifest{Class: CharacterClass, Interfaces: []string{"actor", "narrative-character"}, Words: map[string]introspect.WordSpec{
		TypePropose: {Description: "Propose an action using only this character actor's private beliefs.", ErrorCodes: []string{"bad_payload"}},
		TypeObserve: {Description: "Observe one world fact and commit it to this character actor's private durable beliefs.", ErrorCodes: []string{"bad_payload", "state_failed"}},
	}}
}

func environmentManifest() introspect.Manifest {
	return introspect.Manifest{Class: EnvironmentClass, Interfaces: []string{"actor", "narrative-system"}, Words: map[string]introspect.WordSpec{
		TypeSystemPropose: {Description: "Propose environment transitions from the current world snapshot.", ErrorCodes: []string{"bad_payload"}},
	}}
}

func projectorManifest() introspect.Manifest {
	return introspect.Manifest{Class: ProjectorClass, Interfaces: []string{"actor", "narrative-projector"}, Words: map[string]introspect.WordSpec{
		TypeProject:      {Description: "Accumulate completed public facts and publish only a source-complete long-form chapter.", ErrorCodes: []string{"bad_payload", "state_failed"}},
		TypeProjectReset: {Description: "Clear only the projector's unfinished scene buffer before an audited ledger replay.", ErrorCodes: []string{"state_failed"}},
	}}
}

func plannerManifest() introspect.Manifest {
	return introspect.Manifest{Class: PlannerClass, Interfaces: []string{"actor", "narrative-planner"}, Words: map[string]introspect.WordSpec{TypePlan: {Description: "Compile causally linked facts into a durable dramatic scene plan.", ErrorCodes: []string{"bad_payload", "state_failed"}}}}
}
func writerManifest() introspect.Manifest {
	return introspect.Manifest{Class: WriterClass, Interfaces: []string{"actor", "narrative-writer"}, Words: map[string]introspect.WordSpec{TypeWrite: {Description: "Write or revise one whole chapter from an immutable scene plan.", ErrorCodes: []string{"bad_payload", "generation_failed", "state_failed"}}}}
}
func criticManifest() introspect.Manifest {
	return introspect.Manifest{Class: CriticClass, Interfaces: []string{"actor", "narrative-critic"}, Words: map[string]introspect.WordSpec{TypeCritique: {Description: "Independently review causality, continuity, repetition, provenance and publication readiness.", ErrorCodes: []string{"bad_payload", "critique_failed", "state_failed"}}}}
}
