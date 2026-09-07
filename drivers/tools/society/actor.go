package society

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/society/model"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/lib/behavior"
	"github.com/wanpengxie/atoll/protocol/message"
	"github.com/wanpengxie/atoll/protocol/resource"
	"github.com/wanpengxie/atoll/runtime/schedule"
)

const (
	TypeStatus     = "society.experiment.status"
	TypeStart      = "society.experiment.start"
	TypePause      = "society.experiment.pause"
	TypeResume     = "society.experiment.resume"
	TypeStep       = "society.experiment.step"
	TypeStop       = "society.experiment.stop"
	TypeReset      = "society.experiment.reset"
	TypeIntervene  = "society.experiment.intervene"
	TypeSnapshot   = "society.experiment.snapshot"
	TypeHistory    = "society.experiment.history"
	TypeSpeed      = "society.experiment.speed"
	TypeTrace      = "society.experiment.trace"
	TypePopulation = "society.experiment.population"
	TypeNetwork    = "society.experiment.network"
	TypeInspect    = "society.experiment.inspect"
	TypeRecent     = "society.experiment.recent"
	TypeExport     = "society.experiment.export"
	typeClockTick  = "society.clock.tick"
	typeAuditDay   = "society.audit.day"
)

const stateKey resource.ResourceID = "society.experiment"

type storedState struct {
	Checkpoint      model.Checkpoint    `json:"checkpoint"`
	AppliedCommands []string            `json:"applied_commands"`
	PendingAudits   []model.AuditBundle `json:"pending_audits,omitempty"`
	TickIntervalMS  int                 `json:"tick_interval_ms,omitempty"`
}

type runtimeState struct {
	engine   *model.Engine
	applied  map[string]struct{}
	commands []string
	pending  []model.AuditBundle
	tickMS   int
}

type commandPayload struct {
	CommandID string `json:"command_id"`
}

type stepPayload struct {
	CommandID string `json:"command_id"`
	Days      int    `json:"days"`
}

type intervenePayload struct {
	CommandID    string             `json:"command_id"`
	Intervention model.Intervention `json:"intervention"`
}

type tickPayload struct {
	ExpectedDay int `json:"expected_day"`
}

type historyPayload struct {
	FromDay int `json:"from_day"`
	ToDay   int `json:"to_day"`
	Stride  int `json:"stride"`
}

type speedPayload struct {
	CommandID      string `json:"command_id"`
	TickIntervalMS int    `json:"tick_interval_ms"`
}

type tracePayload struct {
	SubjectID string `json:"subject_id"`
	Limit     int    `json:"limit"`
}

type inspectPayload struct {
	SubjectID string `json:"subject_id"`
}

type populationResponse struct {
	Day      int     `json:"day"`
	Citizens [][]any `json:"citizens"`
	Firms    [][]any `json:"firms"`
}

type networkResponse struct {
	Day   int     `json:"day"`
	Edges [][]any `json:"edges"`
}

type inspectResponse struct {
	Citizen       model.Citizen        `json:"citizen"`
	Balance       model.Money          `json:"balance"`
	Household     *model.Household     `json:"household,omitempty"`
	Relationships []model.Relationship `json:"relationships"`
}

type recentPayload struct {
	Limit int `json:"limit"`
}

type statusResponse struct {
	model.StateSnapshot
	TickIntervalMS int `json:"tick_interval_ms"`
}

func Def(cfg Config) actorbase.Def {
	return actorbase.Def{Manifest: manifest(), New: func() (actorbase.Proc, error) {
		return func(sys actorbase.Sys) error { return run(sys, cfg) }, nil
	}}
}

func run(sys actorbase.Sys, cfg Config) error {
	state, err := loadOrCreate(sys, cfg)
	if err != nil {
		return err
	}
	if err := flushPending(sys, state); err != nil {
		return err
	}
	if !state.engine.Clock().Paused && !state.engine.Clock().Stopped {
		if err := armTick(sys, state.tickMS, state.engine.Clock().Day+1); err != nil {
			return err
		}
	}
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
		}
		if msg.Kind == message.KindEvent {
			if msg.Type == typeClockTick {
				handleTick(sys, cfg, state, msg)
			}
			continue
		}
		handleRequest(sys, cfg, state, msg)
	}
}

func loadOrCreate(sys actorbase.Sys, cfg Config) (*runtimeState, error) {
	out, err := sys.State().Get(stateKey)
	if err != nil {
		return nil, fmt.Errorf("society: load state: %w", err)
	}
	if out.Found {
		var stored storedState
		if err := json.Unmarshal(out.Value, &stored); err != nil {
			return nil, fmt.Errorf("society: decode state: %w", err)
		}
		engine, err := model.Restore(stored.Checkpoint)
		if err != nil {
			return nil, err
		}
		tickMS := stored.TickIntervalMS
		if tickMS == 0 {
			tickMS = cfg.TickIntervalMS
		}
		state := &runtimeState{engine: engine, applied: map[string]struct{}{}, commands: append([]string(nil), stored.AppliedCommands...), pending: append([]model.AuditBundle(nil), stored.PendingAudits...), tickMS: tickMS}
		for _, id := range state.commands {
			state.applied[id] = struct{}{}
		}
		return state, nil
	}
	modelCfg, err := cfg.modelConfig()
	if err != nil {
		return nil, err
	}
	engine, err := model.New(modelCfg)
	if err != nil {
		return nil, err
	}
	engine.Pause()
	state := &runtimeState{engine: engine, applied: map[string]struct{}{}, tickMS: cfg.TickIntervalMS}
	if err := persist(sys, state); err != nil {
		return nil, err
	}
	return state, nil
}

func persist(sys actorbase.Sys, state *runtimeState) error {
	raw, err := json.Marshal(storedState{Checkpoint: state.engine.Checkpoint(), AppliedCommands: state.commands, PendingAudits: state.pending, TickIntervalMS: state.tickMS})
	if err != nil {
		return err
	}
	out, err := sys.State().Put(stateKey, raw)
	if err != nil {
		return fmt.Errorf("society: persist state: %w", err)
	}
	if !out.Accepted() {
		return fmt.Errorf("society: persist state rejected: %s", out.RejectReason)
	}
	return nil
}

func handleRequest(sys actorbase.Sys, cfg Config, state *runtimeState, msg actorbase.Msg) {
	switch msg.Type {
	case TypeStatus, TypeSnapshot:
		if !decodeEmpty(sys, msg) {
			return
		}
		_, _ = sys.Reply(msg, currentStatus(state))
	case TypeHistory:
		var payload historyPayload
		if !decode(sys, msg, &payload) {
			return
		}
		history, err := state.engine.MetricsHistory(payload.FromDay, payload.ToDay, payload.Stride)
		if err != nil {
			_, _ = sys.Fail(msg, "invalid_args", err.Error())
			return
		}
		_, _ = sys.Reply(msg, history)
	case TypeSpeed:
		handleSpeed(sys, state, msg)
	case TypeTrace:
		var payload tracePayload
		if !decode(sys, msg, &payload) {
			return
		}
		trace, err := state.engine.SubjectTrace(payload.SubjectID, payload.Limit)
		if err != nil {
			_, _ = sys.Fail(msg, "invalid_args", err.Error())
			return
		}
		_, _ = sys.Reply(msg, trace)
	case TypePopulation:
		if !decodeEmpty(sys, msg) {
			return
		}
		_, _ = sys.Reply(msg, currentPopulation(state))
	case TypeNetwork:
		if !decodeEmpty(sys, msg) {
			return
		}
		_, _ = sys.Reply(msg, currentNetwork(state))
	case TypeInspect:
		var payload inspectPayload
		if !decode(sys, msg, &payload) {
			return
		}
		inspected, found := inspectCitizen(state, payload.SubjectID)
		if !found {
			_, _ = sys.Fail(msg, "not_found", "citizen not found")
			return
		}
		_, _ = sys.Reply(msg, inspected)
	case TypeRecent:
		var payload recentPayload
		if !decode(sys, msg, &payload) {
			return
		}
		recent, err := state.engine.RecentHistory(payload.Limit)
		if err != nil {
			_, _ = sys.Fail(msg, "invalid_args", err.Error())
			return
		}
		_, _ = sys.Reply(msg, recent)
	case TypeExport:
		if !decodeEmpty(sys, msg) {
			return
		}
		exported, err := state.engine.BrowserExport()
		if err != nil {
			_, _ = sys.Fail(msg, "export_error", err.Error())
			return
		}
		_, _ = sys.Reply(msg, exported)
	case TypeStart, TypeResume:
		var payload commandPayload
		if !decode(sys, msg, &payload) {
			return
		}
		if state.engine.Clock().Stopped {
			_, _ = sys.Fail(msg, "experiment_stopped", "reset the experiment before starting it again")
			return
		}
		state.engine.Resume()
		if err := persist(sys, state); err != nil {
			_, _ = sys.Fail(msg, "state_error", err.Error())
			return
		}
		if err := armTick(sys, state.tickMS, state.engine.Clock().Day+1); err != nil {
			_, _ = sys.Fail(msg, "timer_error", err.Error())
			return
		}
		_, _ = sys.Reply(msg, state.engine.Summary())
	case TypePause:
		var payload commandPayload
		if !decode(sys, msg, &payload) {
			return
		}
		state.engine.Pause()
		if err := persist(sys, state); err != nil {
			_, _ = sys.Fail(msg, "state_error", err.Error())
			return
		}
		_, _ = sys.Reply(msg, state.engine.Summary())
	case TypeStop:
		var payload commandPayload
		if !decode(sys, msg, &payload) {
			return
		}
		state.engine.Stop()
		if err := persist(sys, state); err != nil {
			_, _ = sys.Fail(msg, "state_error", err.Error())
			return
		}
		_, _ = sys.Reply(msg, state.engine.Summary())
	case TypeStep:
		handleStep(sys, state, msg)
	case TypeReset:
		handleReset(sys, cfg, state, msg)
	case TypeIntervene:
		handleIntervene(sys, state, msg)
	default:
		_, _ = sys.Fail(msg, "type_unsupported", fmt.Sprintf("society actor does not answer %q", msg.Type))
	}
}

func handleSpeed(sys actorbase.Sys, state *runtimeState, msg actorbase.Msg) {
	var payload speedPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if payload.CommandID == "" || payload.TickIntervalMS < 1 || payload.TickIntervalMS > 60_000 {
		_, _ = sys.Fail(msg, "invalid_args", "command_id is required and tick_interval_ms must be in [1,60000]")
		return
	}
	if _, duplicate := state.applied[payload.CommandID]; duplicate {
		_, _ = sys.Reply(msg, currentStatus(state))
		return
	}
	state.tickMS = payload.TickIntervalMS
	remember(state, payload.CommandID)
	if err := persist(sys, state); err != nil {
		_, _ = sys.Fail(msg, "state_error", err.Error())
		return
	}
	_, _ = sys.Reply(msg, currentStatus(state))
}

func currentStatus(state *runtimeState) statusResponse {
	return statusResponse{StateSnapshot: state.engine.StateSnapshot(), TickIntervalMS: state.tickMS}
}

// currentPopulation is deliberately tuple-shaped so the complete browser
// working set stays below the gateway's bounded feed projection. Tuple fields:
// citizen=[id,alive,employer_id,balance], firm=[id,active,employee_count,inventory,balance].
func currentPopulation(state *runtimeState) populationResponse {
	snapshot := state.engine.StateSnapshot()
	citizens := make([][]any, 0, len(snapshot.Citizens))
	for _, citizen := range snapshot.Citizens {
		citizens = append(citizens, []any{citizen.ID, citizen.Alive, citizen.EmployerID, snapshot.Balances[citizen.AccountID]})
	}
	firms := make([][]any, 0, len(snapshot.Firms))
	for _, firm := range snapshot.Firms {
		firms = append(firms, []any{firm.ID, firm.Active, len(firm.Employees), firm.Inventory, snapshot.Balances[firm.AccountID]})
	}
	return populationResponse{Day: snapshot.Clock.Day, Citizens: citizens, Firms: firms}
}

// currentNetwork encodes endpoints as citizen indexes and relationship kind as
// 0=social, 1=family. This preserves the full graph without crossing the feed
// projection budget.
func currentNetwork(state *runtimeState) networkResponse {
	snapshot := state.engine.StateSnapshot()
	indexes := make(map[string]int, len(snapshot.Citizens))
	for index, citizen := range snapshot.Citizens {
		indexes[citizen.ID] = index
	}
	edges := make([][]any, 0, len(snapshot.Relationships))
	for _, relation := range snapshot.Relationships {
		a, aOK := indexes[relation.A]
		b, bOK := indexes[relation.B]
		if !aOK || !bOK {
			continue
		}
		kind := 0
		if relation.Kind == "family" {
			kind = 1
		}
		edges = append(edges, []any{a, b, relation.TrustBPS, kind})
	}
	return networkResponse{Day: snapshot.Clock.Day, Edges: edges}
}

func inspectCitizen(state *runtimeState, subjectID string) (inspectResponse, bool) {
	snapshot := state.engine.StateSnapshot()
	for _, citizen := range snapshot.Citizens {
		if citizen.ID != subjectID {
			continue
		}
		result := inspectResponse{Citizen: citizen, Balance: snapshot.Balances[citizen.AccountID]}
		for index := range snapshot.Households {
			if snapshot.Households[index].ID == citizen.HouseholdID {
				household := snapshot.Households[index]
				result.Household = &household
				break
			}
		}
		for _, relation := range snapshot.Relationships {
			if relation.A == citizen.ID || relation.B == citizen.ID {
				result.Relationships = append(result.Relationships, relation)
			}
		}
		return result, true
	}
	return inspectResponse{}, false
}

func handleStep(sys actorbase.Sys, state *runtimeState, msg actorbase.Msg) {
	var payload stepPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if payload.CommandID == "" {
		_, _ = sys.Fail(msg, "invalid_args", "command_id is required for idempotent mutation")
		return
	}
	if payload.Days < 1 || payload.Days > 365 {
		_, _ = sys.Fail(msg, "invalid_args", "days must be in [1,365]")
		return
	}
	if _, duplicate := state.applied[payload.CommandID]; duplicate {
		_ = flushPending(sys, state)
		_, _ = sys.Reply(msg, state.engine.Summary())
		return
	}
	for i := 0; i < payload.Days; i++ {
		if state.engine.Clock().Day >= state.engine.Config().Days {
			state.engine.Stop()
			break
		}
		if err := advanceAudited(state); err != nil {
			_, _ = sys.Fail(msg, "experiment_error", err.Error())
			return
		}
	}
	if state.engine.Clock().Day >= state.engine.Config().Days {
		state.engine.Stop()
	}
	remember(state, payload.CommandID)
	if err := persist(sys, state); err != nil {
		_, _ = sys.Fail(msg, "state_error", err.Error())
		return
	}
	_ = flushPending(sys, state)
	emitDay(sys, msg.Cause(), state.engine.Summary())
	_, _ = sys.Reply(msg, state.engine.Summary())
}

func handleReset(sys actorbase.Sys, cfg Config, state *runtimeState, msg actorbase.Msg) {
	var payload commandPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if payload.CommandID == "" {
		_, _ = sys.Fail(msg, "invalid_args", "command_id is required for idempotent mutation")
		return
	}
	if _, duplicate := state.applied[payload.CommandID]; duplicate {
		_, _ = sys.Reply(msg, state.engine.Summary())
		return
	}
	modelCfg, err := cfg.modelConfig()
	if err != nil {
		_, _ = sys.Fail(msg, "config_error", err.Error())
		return
	}
	engine, err := model.New(modelCfg)
	if err != nil {
		_, _ = sys.Fail(msg, "experiment_error", err.Error())
		return
	}
	engine.Pause()
	state.engine = engine
	remember(state, payload.CommandID)
	if err := persist(sys, state); err != nil {
		_, _ = sys.Fail(msg, "state_error", err.Error())
		return
	}
	_, _ = sys.Reply(msg, state.engine.Summary())
}

func handleIntervene(sys actorbase.Sys, state *runtimeState, msg actorbase.Msg) {
	var payload intervenePayload
	if !decode(sys, msg, &payload) {
		return
	}
	if payload.CommandID == "" || payload.Intervention.ID == "" {
		_, _ = sys.Fail(msg, "invalid_args", "command_id and intervention.id are required")
		return
	}
	if _, duplicate := state.applied[payload.CommandID]; duplicate {
		_ = flushPending(sys, state)
		_, _ = sys.Reply(msg, state.engine.Summary())
		return
	}
	events, transactions := state.engine.HistoryCounts()
	if _, err := state.engine.Intervene(payload.Intervention); err != nil {
		_, _ = sys.Fail(msg, "invalid_args", err.Error())
		return
	}
	state.pending = append(state.pending, state.engine.AuditSince(events, transactions))
	remember(state, payload.CommandID)
	if err := persist(sys, state); err != nil {
		_, _ = sys.Fail(msg, "state_error", err.Error())
		return
	}
	_ = flushPending(sys, state)
	raw, _ := json.Marshal(payload.Intervention)
	_, _ = sys.Emit(behavior.EventSpec{Type: "society.policy.changed", Payload: raw, Cause: msg.Cause()})
	_, _ = sys.Reply(msg, state.engine.Summary())
}

func handleTick(sys actorbase.Sys, cfg Config, state *runtimeState, msg actorbase.Msg) {
	var payload tickPayload
	if json.Unmarshal(msg.Payload, &payload) != nil {
		return
	}
	clock := state.engine.Clock()
	if clock.Paused || clock.Stopped || payload.ExpectedDay != clock.Day+1 {
		return
	}
	if clock.Day >= state.engine.Config().Days {
		state.engine.Stop()
		_ = persist(sys, state)
		return
	}
	if err := advanceAudited(state); err != nil {
		_ = sys.PublishObs("society.tick.error", []byte(err.Error()))
		return
	}
	if state.engine.Clock().Day >= state.engine.Config().Days {
		state.engine.Stop()
	}
	if err := persist(sys, state); err != nil {
		_ = sys.PublishObs("society.state.error", []byte(err.Error()))
		return
	}
	_ = flushPending(sys, state)
	emitDay(sys, msg.Cause(), state.engine.Summary())
	if !state.engine.Clock().Stopped {
		_ = armTick(sys, state.tickMS, state.engine.Clock().Day+1)
	}
}

func advanceAudited(state *runtimeState) error {
	events, transactions := state.engine.HistoryCounts()
	if _, err := state.engine.Advance(); err != nil {
		return err
	}
	state.pending = append(state.pending, state.engine.AuditSince(events, transactions))
	return nil
}

// flushPending is a small durable outbox. State containing the audit is saved
// before this runs; a crash may therefore redeliver a deterministic bundle but
// cannot silently lose the day's facts. Consumers deduplicate by bundle ID.
func flushPending(sys actorbase.Sys, state *runtimeState) error {
	for len(state.pending) > 0 {
		raw, err := json.Marshal(state.pending[0])
		if err != nil {
			return err
		}
		if _, err := sys.Emit(behavior.EventSpec{Type: typeAuditDay, Payload: raw, Cause: message.Root()}); err != nil {
			return err
		}
		state.pending = state.pending[1:]
		if err := persist(sys, state); err != nil {
			return err
		}
	}
	return nil
}

func armTick(sys actorbase.Sys, tickIntervalMS, expected int) error {
	_, err := sys.After(time.Duration(tickIntervalMS)*time.Millisecond, typeClockTick, tickPayload{ExpectedDay: expected}, schedule.TimerHomeDurable)
	return err
}

func emitDay(sys actorbase.Sys, cause message.Cause, summary model.Summary) {
	raw, _ := json.Marshal(summary)
	_, _ = sys.Emit(behavior.EventSpec{Type: "society.day.completed", Payload: raw, Cause: cause})
	_ = sys.PublishObs("society.metrics", raw)
}

func remember(state *runtimeState, id string) {
	state.applied[id] = struct{}{}
	state.commands = append(state.commands, id)
}

func decodeEmpty(sys actorbase.Sys, msg actorbase.Msg) bool {
	var payload struct{}
	return decode(sys, msg, &payload)
}

func decode(sys actorbase.Sys, msg actorbase.Msg, target any) bool {
	if err := actorbase.DecodeStrict(msg.Payload, target); err != nil {
		_, _ = sys.Fail(msg, "invalid_args", err.Error())
		return false
	}
	return true
}
