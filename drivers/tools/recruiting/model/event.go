package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EventIntent is the pure-domain handoff to the repository/outbox boundary.
// Business time is supplied by the caller; the model never reads wall clock.
type EventIntent struct {
	EventID          string          `json:"event_id"`
	Kind             string          `json:"kind"`
	AggregateType    string          `json:"aggregate_type"`
	AggregateID      string          `json:"aggregate_id"`
	AggregateVersion uint64          `json:"aggregate_version"`
	BusinessAt       string          `json:"business_at"`
	CauseCommandID   string          `json:"cause_command_id"`
	Payload          json.RawMessage `json:"payload"`
}

func NewEventIntent(eventID, kind, aggregateType, aggregateID string, aggregateVersion uint64, businessAt, causeCommandID string, payload json.RawMessage) (EventIntent, error) {
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(kind) == "" || strings.TrimSpace(aggregateType) == "" ||
		strings.TrimSpace(aggregateID) == "" || aggregateVersion == 0 || strings.TrimSpace(causeCommandID) == "" || !json.Valid(payload) {
		return EventIntent{}, fmt.Errorf("complete event identity, aggregate, command cause, and valid payload are required")
	}
	if _, err := time.Parse(time.RFC3339, businessAt); err != nil {
		return EventIntent{}, fmt.Errorf("business_at must be RFC3339: %w", err)
	}
	return EventIntent{
		EventID: eventID, Kind: kind, AggregateType: aggregateType, AggregateID: aggregateID,
		AggregateVersion: aggregateVersion, BusinessAt: businessAt, CauseCommandID: causeCommandID,
		Payload: append(json.RawMessage(nil), payload...),
	}, nil
}
