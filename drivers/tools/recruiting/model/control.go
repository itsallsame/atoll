package model

import "fmt"

type ControlStatus string

const (
	ControlActive   ControlStatus = "active"
	ControlPaused   ControlStatus = "paused"
	ControlArchived ControlStatus = "archived"
)

type PauseMode string

const (
	PauseDrain             PauseMode = "drain"
	PauseFinishCausalChain PauseMode = "finish_causal_chain"
	PauseCancel            PauseMode = "cancel"
)

func (m PauseMode) Validate() error {
	switch m {
	case PauseDrain, PauseFinishCausalChain, PauseCancel:
		return nil
	default:
		return fmt.Errorf("unknown pause mode %q", m)
	}
}

type HealthStatus string

const (
	HealthHealthy     HealthStatus = "healthy"
	HealthDegraded    HealthStatus = "degraded"
	HealthCircuitOpen HealthStatus = "circuit_open"
)
