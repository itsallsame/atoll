package mcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/runtime/actorrt"
	"github.com/wanpengxie/atoll/runtime/schedule"
)

const controlAttempts = 8

type manifestPublicationError struct{ err error }

func (e *manifestPublicationError) Error() string {
	return "MCP manifest publication unconfirmed: " + e.err.Error()
}
func (e *manifestPublicationError) Unwrap() error { return e.err }

func (a *mcpActor) refreshCycle(sys actorbase.Sys) error {
	err := a.refresh(sys, sys.Life())
	var publicationErr *manifestPublicationError
	if errors.As(err, &publicationErr) {
		// Unknown may mean the write landed but its acknowledgement did not.
		// After the retry budget, neither the old nor new call table can be
		// asserted to match public state. End this body instead of serving it.
		return err
	}
	// Discovery failures have not written state. Preserve the last published
	// table and keep probing through the existing scheduled refresh path.
	return a.armRefresh(sys)
}

// This is bounded backoff for publishing through existing capabilities, not
// another refresh scheduler. Steady-state refresh still uses Sys.After. The
// body may start before its outbound stream is connected; nil error alone is
// not an acknowledgement of a State.Put in that window.
func retryControl(ctx context.Context, attempt func() error) error {
	delay := 100 * time.Millisecond
	var err error
	for n := 0; n < controlAttempts; n++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = attempt(); err == nil {
			return nil
		}
		if n == controlAttempts-1 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, time.Second)
	}
	return fmt.Errorf("MCP control operation failed after %d attempts: %w", controlAttempts, err)
}

type refreshPayload struct {
	Token string `json:"token"`
}

func (a *mcpActor) armRefresh(sys actorbase.Sys) error {
	// An unconfirmed Schedule may already have created a timer. All retries
	// carry one token; only its first delivery may refresh/rearm. A fresh token
	// also makes timers surviving body replacement harmless to the new body.
	a.refreshToken = rand.Text()
	payload := refreshPayload{Token: a.refreshToken}
	err := retryControl(sys.Life(), func() error {
		_, err := sys.After(refreshInterval, typeRefresh, payload, schedule.TimerHomeMemory)
		return err
	})
	publishControlObs(sys, "mcp.refresh_schedule", err)
	if err != nil {
		a.refreshToken = ""
		// Do not leave a live adapter with no known future refresh. Returning
		// ends this Proc through the existing host lifecycle and reports why.
		return fmt.Errorf("MCP refresh timer unavailable: %w", err)
	}
	return nil
}

func (a *mcpActor) consumeRefresh(sys actorbase.Sys, msg actorbase.Msg) bool {
	var payload refreshPayload
	if msg.Sender.ID != sys.Self() || actorbase.DecodeStrict(msg.Payload, &payload) != nil ||
		a.refreshToken == "" || payload.Token != a.refreshToken {
		return false
	}
	a.refreshToken = ""
	return true
}

func publishControlObs(sys actorbase.Sys, kind actorrt.ObsKind, err error) {
	value := map[string]string{"status": "ready"}
	if err != nil {
		value["status"] = "failed"
		value["detail"] = err.Error()
	}
	raw, _ := json.Marshal(value)
	_ = sys.PublishObs(kind, raw)
}
