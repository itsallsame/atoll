package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/access"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
	"github.com/wanpengxie/atoll/protocol/resource"
	"github.com/wanpengxie/atoll/runtime/accessdoor"
	"github.com/wanpengxie/atoll/runtime/actorrt"
	"github.com/wanpengxie/atoll/runtime/schedule"
)

func (*terminalRecorder) PublishObs(actorrt.ObsKind, actorrt.ObsValue) error { return nil }

type publicationState struct {
	discardState
	put func([]byte) (accessdoor.Outcome, error)
}

func (s publicationState) Put(id resource.ResourceID, raw []byte) (accessdoor.Outcome, error) {
	if id != actorbase.ManifestStateKey {
		return accessdoor.Outcome{}, fmt.Errorf("unexpected state key %s", id)
	}
	return s.put(raw)
}

type recoverySys struct {
	terminalRecorder
	ctx   context.Context
	state publicationState
	after func(refreshPayload) (schedule.TimerID, error)
	obs   map[actorrt.ObsKind]map[string]string
}

func (s *recoverySys) State() actorbase.StateHandle { return s.state }
func (s *recoverySys) Life() context.Context        { return s.ctx }
func (s *recoverySys) PublishObs(kind actorrt.ObsKind, raw actorrt.ObsValue) error {
	if s.obs == nil {
		s.obs = make(map[actorrt.ObsKind]map[string]string)
	}
	var value map[string]string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	s.obs[kind] = value
	return nil
}
func (s *recoverySys) After(d time.Duration, typ string, payload any, home schedule.TimerHome) (schedule.TimerID, error) {
	if d != refreshInterval || typ != typeRefresh || home != schedule.TimerHomeMemory {
		panic("refresh left the existing scheduler contract")
	}
	return s.after(payload.(refreshPayload))
}

func publicationActor() *mcpActor {
	transport := &handlerTransport{calls: make(map[string]int)}
	transport.handle = func(q rpcRequest) (json.RawMessage, *rpcError, error) {
		switch q.Method {
		case "server/discover":
			return json.RawMessage(`{"supportedVersions":["2026-07-28"],"ttlMs":0}`), nil, nil
		case "tools/list":
			return json.RawMessage(`{"tools":[{"name":"ping","inputSchema":{"type":"object"}}],"ttlMs":0}`), nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected method %s", q.Method)
	}
	return &mcpActor{cfg: Config{Name: "fixture"}, client: testClient(transport)}
}

func TestManifestPublicationRecoversFromUnconfirmedStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := publicationActor()
		sys := &recoverySys{ctx: context.Background()}
		puts := 0
		var persisted map[string]json.RawMessage
		sys.state.put = func(raw []byte) (accessdoor.Outcome, error) {
			puts++
			if len(a.snapshot.tools) != 0 {
				t.Fatal("call table activated before publication was accepted")
			}
			// Simulate an applied write whose ack was lost: replaying the same
			// manifest must be safe, and still must await a confirmed outcome.
			if err := json.Unmarshal(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			if puts <= 2 {
				return accessdoor.Outcome{RejectReason: access.OutcomeUnknown}, nil
			}
			return accessdoor.Outcome{}, nil
		}
		if err := a.refresh(sys, sys.Life()); err != nil {
			t.Fatal(err)
		}
		if puts != 3 || a.snapshot.tools["fixture.ping"] != "ping" || persisted["fixture.ping"] == nil || a.currentLastError() != nil {
			t.Fatalf("puts=%d snapshot=%+v error=%v", puts, a.snapshot, a.currentLastError())
		}
		if sys.obs["mcp.manifest"]["status"] != "ready" {
			t.Fatalf("obs=%v", sys.obs)
		}
	})
}

func TestManifestPublicationExhaustionPreservesLastPublishedTableAndRecovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := publicationActor()
		a.snapshot = snapshot{tools: map[string]string{"fixture.old": "old"}}
		sys := &recoverySys{ctx: context.Background()}
		puts := 0
		sys.state.put = func([]byte) (accessdoor.Outcome, error) {
			puts++
			return accessdoor.Outcome{RejectReason: access.OutcomeUnknown}, nil
		}
		err := a.refreshCycle(sys)
		var publicationErr *manifestPublicationError
		if !errors.As(err, &publicationErr) {
			t.Fatalf("unconfirmed publication did not terminate cycle: %v", err)
		}
		if puts != controlAttempts || a.currentLastError() == nil || len(a.snapshot.tools) != 1 || a.snapshot.tools["fixture.old"] != "old" {
			t.Fatalf("puts=%d snapshot=%+v error=%v", puts, a.snapshot, a.currentLastError())
		}
		if sys.obs["mcp.manifest"]["status"] != "failed" {
			t.Fatalf("obs=%v", sys.obs)
		}
		// A later publication attempt can recover; the old error is not a gate.
		sys.state.put = func([]byte) (accessdoor.Outcome, error) { return accessdoor.Outcome{}, nil }
		a.refresh(sys, sys.Life())
		if len(a.snapshot.tools) != 1 || a.snapshot.tools["fixture.ping"] != "ping" || a.currentLastError() != nil {
			t.Fatalf("recovery snapshot=%+v error=%v", a.snapshot, a.currentLastError())
		}
	})
}

func refreshEvent(token string, sender actor.ActorID) actorbase.Msg {
	raw, _ := json.Marshal(refreshPayload{Token: token})
	return actorbase.NewMsg(actorbase.OriginMailbox, context.Background(), message.Envelope{
		Kind: message.KindEvent, Type: typeRefresh, Sender: message.Sender{ID: sender}, Payload: raw,
	})
}

func TestRefreshRegistrationRecoversWithoutMultiplyingRefreshLoops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := publicationActor()
		sys := &recoverySys{ctx: context.Background()}
		var registered []refreshPayload
		sys.after = func(p refreshPayload) (schedule.TimerID, error) {
			registered = append(registered, p)
			if len(registered) == 1 {
				return "", errors.New("registration ack lost")
			}
			return "timer", nil
		}
		if err := a.armRefresh(sys); err != nil {
			t.Fatal(err)
		}
		if len(registered) != 2 || registered[0].Token != registered[1].Token {
			t.Fatalf("registrations=%v", registered)
		}
		event := refreshEvent(registered[0].Token, sys.Self())
		if a.consumeRefresh(sys, refreshEvent(registered[0].Token, "tool:other")) {
			t.Fatal("accepted another actor's timer")
		}
		if !a.consumeRefresh(sys, event) {
			t.Fatal("lost the first timer delivery")
		}
		if err := a.armRefresh(sys); err != nil {
			t.Fatal(err)
		}
		if a.consumeRefresh(sys, event) {
			t.Fatal("duplicate timer started a second refresh loop")
		}
		if a.consumeRefresh(sys, refreshEvent("", sys.Self())) {
			t.Fatal("accepted legacy timer from a previous body")
		}
		if !a.consumeRefresh(sys, refreshEvent(a.refreshToken, sys.Self())) {
			t.Fatal("new refresh cycle did not run")
		}
	})
}

func TestRefreshRegistrationExhaustionAndStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint("stop=", stop), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				a := publicationActor()
				sys := &recoverySys{ctx: ctx}
				calls := 0
				sys.after = func(refreshPayload) (schedule.TimerID, error) { calls++; return "", errors.New("disconnected") }
				if stop {
					go func() { time.Sleep(10 * time.Millisecond); cancel() }()
				}
				err := a.armRefresh(sys)
				want := controlAttempts
				if stop {
					want = 1
				}
				if err == nil || calls != want || a.refreshToken != "" || sys.obs["mcp.refresh_schedule"]["status"] != "failed" {
					t.Fatalf("calls=%d token=%q err=%v obs=%v", calls, a.refreshToken, err, sys.obs)
				}
				if stop && !errors.Is(err, context.Canceled) {
					t.Fatalf("stop error=%v", err)
				}
			})
		})
	}
}

func TestDiscoveryOutageKeepsPublishedCallsAndSchedulesRecovery(t *testing.T) {
	a := publicationActor()
	a.snapshot = snapshot{tools: map[string]string{"fixture.old": "old"}}
	a.client.transport.(*handlerTransport).handle = func(rpcRequest) (json.RawMessage, *rpcError, error) {
		return nil, nil, errors.New("MCP temporarily offline")
	}
	sys := &recoverySys{ctx: context.Background()}
	armed := 0
	sys.after = func(refreshPayload) (schedule.TimerID, error) { armed++; return "timer", nil }
	if err := a.refreshCycle(sys); err != nil {
		t.Fatal(err)
	}
	if armed != 1 || a.snapshot.tools["fixture.old"] != "old" || a.currentLastError() == nil {
		t.Fatalf("armed=%d snapshot=%v lastError=%v", armed, a.snapshot, a.currentLastError())
	}
}
