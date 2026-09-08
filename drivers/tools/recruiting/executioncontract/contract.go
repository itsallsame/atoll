// Package executioncontract defines the recruiting control-plane/executor
// wire contract. It is an application contract: Atoll transports the payload
// without knowing or changing any recruiting semantics.
package executioncontract

import "github.com/wanpengxie/atoll/drivers/tools/recruiting/model"

const Version = "recruiting.execution.v1"

// Offer is the immutable input accepted by one executor. Kind selects the
// domain payload while Work/Attempt and routing stay uniform, so listing and
// detail execution remain one executor class.
type Offer struct {
	Kind                string                       `json:"kind"`
	Attempt             model.Attempt                `json:"attempt"`
	Work                model.Work                   `json:"work"`
	Occurrence          *model.SourceOccurrence      `json:"occurrence,omitempty"`
	Checkpoint          *model.IncrementalCheckpoint `json:"checkpoint,omitempty"`
	Detail              *DetailInput                 `json:"detail,omitempty"`
	Budget              model.BudgetPermit           `json:"budget"`
	BudgetExpiresAt     string                       `json:"budget_expires_at"`
	RequestedCapability string                       `json:"requested_capability"`
	RequestedOrigin     string                       `json:"requested_origin,omitempty"`
	RequestedProfileID  string                       `json:"requested_profile_id,omitempty"`
}

type DetailInput struct {
	Job        model.SourceJob              `json:"job"`
	Assignment model.SourceRecipeAssignment `json:"assignment"`
	Recipe     model.Recipe                 `json:"recipe"`
}
