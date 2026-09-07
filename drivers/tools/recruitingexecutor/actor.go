// Package recruitingexecutor provides the one capability-driven executor
// actor class. P0 executes only a deterministic fixture and never performs
// website I/O.
package recruitingexecutor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/lib/behavior"
	"github.com/wanpengxie/atoll/lib/introspect"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
)

const TypeProbe = "recruiting.execution.probe"

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

func manifest() introspect.Manifest {
	return introspect.Manifest{
		Class: Class, Interfaces: []string{"actor", "recruiting-executor"},
		Words: map[string]introspect.WordSpec{
			TypeProbe: {Description: "execute the deterministic recruiting P0 fixture"},
		},
	}
}

func Def(cfg Config) actorbase.Def {
	return actorbase.Def{Manifest: manifest(), New: func() (actorbase.Proc, error) {
		return func(sys actorbase.Sys) error { return run(sys, cfg) }, nil
	}}
}

func run(sys actorbase.Sys, cfg Config) error {
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
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
			_, _ = sys.Fail(msg, "result_delivery_failed", err.Error())
			continue
		}
		_, _ = sys.Reply(msg, map[string]any{"attempt_id": p.AttemptID, "status": "submitted"})
	}
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
