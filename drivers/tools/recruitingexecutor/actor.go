// Package recruitingexecutor provides the one capability-driven executor
// actor class. P0 executes only a deterministic fixture and never performs
// website I/O.
package recruitingexecutor

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/lib/behavior"
	"github.com/wanpengxie/atoll/lib/introspect"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
)

const (
	TypeProbe = "recruiting.execution.probe"
	TypeWake  = executioncontract.TypeWake
)

type probePayload struct {
	WorkID              string        `json:"work_id"`
	AttemptID           string        `json:"attempt_id"`
	ExpectedWorkVersion uint64        `json:"expected_work_version"`
	ReplyTo             actor.ActorID `json:"reply_to"`
	Note                string        `json:"note,omitempty"`
}

type resultPayload struct {
	WorkID              string `json:"work_id"`
	AttemptID           string `json:"attempt_id"`
	ExpectedWorkVersion uint64 `json:"expected_work_version"`
	Result              string `json:"result"`
}

type wakePayload = executioncontract.WakeRequest

type productionRuntime struct {
	driver       *httpdriver.Driver
	options      executeOfferOptions
	batchOptions companyImportOptions
}

func manifest() introspect.Manifest {
	return introspect.Manifest{
		Class: Class, Interfaces: []string{"actor", "recruiting-executor"},
		Words: map[string]introspect.WordSpec{
			TypeProbe: {Description: "execute the deterministic recruiting P0 fixture"},
			TypeWake:  {Description: "claim and execute at most one available recruiting Work after an explicit control-plane wake"},
		},
	}
}

func Def(cfg Config) actorbase.Def {
	return actorbase.Def{Manifest: manifest(), New: func() (actorbase.Proc, error) {
		return func(sys actorbase.Sys) error { return run(sys, cfg) }, nil
	}}
}

func run(sys actorbase.Sys, cfg Config) error {
	incarnation := "executor-" + uuid.NewString()
	var production *productionRuntime
	if cfg.ExecutionEnabled {
		var err error
		production, err = newProductionRuntime(cfg)
		if err != nil {
			return err
		}
	}
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
		}
		if msg.Type == TypeWake {
			handleWake(sys, cfg, production, incarnation, msg)
			continue
		}
		if msg.Type != TypeProbe {
			_, _ = sys.Fail(msg, "type_unsupported", fmt.Sprintf("recruiting executor does not answer %q", msg.Type))
			continue
		}
		var p probePayload
		if !decode(sys, msg, &p) {
			continue
		}
		if p.WorkID == "" || p.AttemptID == "" || p.ExpectedWorkVersion == 0 || p.ReplyTo == "" {
			_, _ = sys.Fail(msg, "payload_invalid", "work_id, attempt_id, expected_work_version, and reply_to are required")
			continue
		}
		result := "fixture:" + cfg.Capability
		if p.Note != "" {
			result += ":" + p.Note
		}
		raw, _ := json.Marshal(resultPayload{
			WorkID: p.WorkID, AttemptID: p.AttemptID,
			ExpectedWorkVersion: p.ExpectedWorkVersion, Result: result,
		})
		_, err = sys.Post(behavior.RequestSpec{
			ID: message.ID("probe-result-" + p.AttemptID), Type: "recruiting.execution.result",
			Payload: raw, Audience: message.Audience{p.ReplyTo}, Cause: msg.Cause(),
		})
		if err != nil {
			_, _ = sys.Fail(msg, "channel_unavailable", "result delivery failed: "+err.Error())
			continue
		}
		_, _ = sys.Reply(msg, map[string]any{"attempt_id": p.AttemptID, "status": "submitted"})
	}
}

func newProductionRuntime(cfg Config) (*productionRuntime, error) {
	options := executeOfferOptions{
		Artifact: artifactSinkConfig{DeviceName: cfg.ArtifactDeviceName, ChannelName: cfg.ArtifactChannelName,
			Directory: cfg.ArtifactDirectory, AccessScope: cfg.ArtifactAccessScope, Retention: cfg.ArtifactRetention,
			Redacted: cfg.ArtifactRedaction == "redacted", MaxBytes: cfg.ArtifactMaxBytes},
		Now: time.Now,
	}
	if cfg.Capability == "company.import" {
		return &productionRuntime{options: options, batchOptions: companyImportOptions{
			Artifact: options.Artifact, MaxBytes: cfg.BatchMaxBytes, ChunkSize: cfg.BatchChunkSize,
		}}, nil
	}
	robots, err := httpdriver.NewRobotsTxtChecker(httpdriver.RobotsPolicy{Timeout: time.Duration(cfg.RobotsTimeoutMS) * time.Millisecond,
		MaxBytes: cfg.RobotsMaxBytes, CacheTTL: time.Duration(cfg.RobotsCacheTTLMS) * time.Millisecond})
	if err != nil {
		return nil, fmt.Errorf("prepare robots checker: %w", err)
	}
	driver, err := httpdriver.New(httpdriver.Policy{MaxConcurrency: cfg.HTTPMaxConcurrency,
		MinOriginInterval: time.Duration(cfg.HTTPMinOriginIntervalMS) * time.Millisecond, CircuitThreshold: cfg.HTTPCircuitThreshold,
		CircuitCooldown: time.Duration(cfg.HTTPCircuitCooldownMS) * time.Millisecond}, robots)
	if err != nil {
		return nil, fmt.Errorf("prepare HTTP driver: %w", err)
	}
	options.Compliance = httpdriver.ComplianceEvidence{TermsPolicyVersion: cfg.TermsPolicyVersion, TermsReviewedAt: cfg.TermsReviewedAt}
	return &productionRuntime{driver: driver, options: options}, nil
}

func handleWake(sys actorbase.Sys, cfg Config, production *productionRuntime, incarnation string, msg actorbase.Msg) {
	if !cfg.ExecutionEnabled || production == nil {
		_, _ = sys.Fail(msg, "config_error", "production recruiting execution is not enabled")
		return
	}
	if msg.Sender.Kind != actor.KindTool || !executioncontract.TargetMatchesAuthenticatedActor(string(cfg.ControlActorID), string(msg.Sender.ID)) {
		_, _ = sys.Fail(msg, "permission_denied", "only the configured recruiting control actor may wake this executor")
		return
	}
	var payload wakePayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.Origin, payload.ProfileID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.Origin), strings.TrimSpace(payload.ProfileID)
	if payload.CommandID == "" || len(payload.CommandID) > 191 {
		_, _ = sys.Fail(msg, "payload_invalid", "command_id is required and must be at most 191 bytes")
		return
	}
	offer, err := requestExecutionOffer(msg.Ctx(), sys, msg.Cause(), cfg.ControlActorID, string(sys.Self()), executioncontract.OfferRequest{
		CommandID: wakeOfferCommandID(incarnation, payload.CommandID, string(msg.ID)), ExecutorIncarnation: incarnation,
		Capability: cfg.Capability, Origin: payload.Origin, ProfileID: payload.ProfileID,
	}, time.Duration(cfg.ControlWaitMS)*time.Millisecond)
	if err != nil {
		_, _ = sys.Fail(msg, "channel_unavailable", err.Error())
		return
	}
	if offer == nil {
		if err := completeWake(sys, cfg.ControlActorID, msg, payload.CommandID, "idle", "", ""); err != nil {
			_, _ = sys.Fail(msg, "result_unknown", err.Error())
			return
		}
		_, _ = sys.Reply(msg, map[string]any{"status": "idle", "executor_incarnation": incarnation})
		return
	}
	control := messageExecutionControl{caller: sys, cause: msg.Cause(), controlActor: cfg.ControlActorID,
		executorActorID: string(sys.Self()), wait: time.Duration(cfg.ControlWaitMS) * time.Millisecond}
	if offer.Kind == "company_import" {
		err = executeCompanyImportOffer(msg.Ctx(), control, sys.Resource(), *offer, production.batchOptions)
	} else if offer.Kind == "company_import_apply" {
		err = executeCompanyImportApplyOffer(msg.Ctx(), control, *offer)
	} else {
		err = executeOffer(msg.Ctx(), control, sys.Resource(), production.driver, *offer, production.options)
	}
	if err != nil {
		_, _ = sys.Fail(msg, "runtime_failed", err.Error(), map[string]any{"attempt_id": offer.Attempt.AttemptID, "work_id": offer.Work.WorkID})
		return
	}
	if err := completeWake(sys, cfg.ControlActorID, msg, payload.CommandID, "handled", offer.Attempt.AttemptID, offer.Work.WorkID); err != nil {
		_, _ = sys.Fail(msg, "result_unknown", err.Error(), map[string]any{"attempt_id": offer.Attempt.AttemptID, "work_id": offer.Work.WorkID})
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"status": "handled", "attempt_id": offer.Attempt.AttemptID,
		"work_id": offer.Work.WorkID, "kind": offer.Kind, "executor_incarnation": incarnation})
}

func completeWake(sys actorbase.Sys, controlActor actor.ActorID, msg actorbase.Msg, dispatchID, status, attemptID, workID string) error {
	completion := executioncontract.WakeCompletion{DispatchID: dispatchID, DeliveryID: string(msg.ID), Status: status,
		AttemptID: attemptID, WorkID: workID}
	raw, err := json.Marshal(completion)
	if err != nil {
		return err
	}
	_, err = sys.Post(behavior.RequestSpec{ID: message.ID("wake-completed-" + stableWakeDigest(string(msg.ID))),
		Type: executioncontract.TypeWakeCompleted, Payload: raw, Audience: message.Audience{controlActor}, Cause: msg.Cause()})
	return err
}

func wakeOfferCommandID(incarnation, dispatchID, deliveryID string) string {
	sum := sha256.Sum256([]byte("recruiting.execution.wake.v1\n" + incarnation + "\n" + dispatchID + "\n" + deliveryID))
	return fmt.Sprintf("wake-offer-%x", sum[:16])
}

func stableWakeDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:16])
}

func decode(sys actorbase.Sys, msg actorbase.Msg, dst any) bool {
	dec := json.NewDecoder(strings.NewReader(string(msg.Payload)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		_, _ = sys.Fail(msg, "payload_invalid", err.Error())
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			_, _ = sys.Fail(msg, "payload_invalid", "multiple JSON values")
		} else {
			_, _ = sys.Fail(msg, "payload_invalid", err.Error())
		}
		return false
	}
	return true
}
