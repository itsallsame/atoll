package societyconsole

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wanpengxie/atoll/drivers/tools/society/model"
)

const (
	publicVoterCookie = "atoll_society_voter"
	civicRoundDays    = 30
)

type PublicService struct {
	mu           sync.Mutex
	path         string
	engine       *model.Engine
	civic        civicState
	lastAdvanced time.Time
}

type civicBallot struct {
	PublicAI       int `json:"public_ai"`
	TransitionFund int `json:"transition_fund"`
	AIDividend     int `json:"ai_dividend"`
	SubmittedDay   int `json:"submitted_day"`
}

type civicRound struct {
	ID                    int                    `json:"id"`
	StartDay              int                    `json:"start_day"`
	EndDay                int                    `json:"end_day"`
	Ballots               map[string]civicBallot `json:"ballots,omitempty"`
	ClosedDay             int                    `json:"closed_day,omitempty"`
	Winner                string                 `json:"winner,omitempty"`
	AppliedInterventionID string                 `json:"applied_intervention_id,omitempty"`
}

type civicState struct {
	Current civicRound   `json:"current"`
	History []civicRound `json:"history,omitempty"`
}

type publicStoredState struct {
	Checkpoint   model.Checkpoint `json:"checkpoint"`
	Civic        civicState       `json:"civic"`
	LastAdvanced time.Time        `json:"last_advanced"`
}

type publicCall struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type votePayload struct {
	CommandID      string `json:"command_id"`
	PublicAI       int    `json:"public_ai"`
	TransitionFund int    `json:"transition_fund"`
	AIDividend     int    `json:"ai_dividend"`
}

type historyPayload struct {
	FromDay int `json:"from_day"`
	ToDay   int `json:"to_day"`
	Stride  int `json:"stride"`
}
type recentPayload struct {
	Limit int `json:"limit"`
}
type tracePayload struct {
	SubjectID string `json:"subject_id"`
	Limit     int    `json:"limit"`
}
type inspectPayload struct {
	SubjectID string `json:"subject_id"`
}

type civicRoundSummary struct {
	ID                    int            `json:"id"`
	StartDay              int            `json:"start_day"`
	EndDay                int            `json:"end_day"`
	ClosedDay             int            `json:"closed_day,omitempty"`
	VoterCount            int            `json:"voter_count"`
	TotalVotes            int            `json:"total_votes"`
	Totals                map[string]int `json:"totals"`
	Winner                string         `json:"winner,omitempty"`
	AppliedInterventionID string         `json:"applied_intervention_id,omitempty"`
}

type civicIssue struct {
	RoundID       int                `json:"round_id"`
	StartDay      int                `json:"start_day"`
	EndDay        int                `json:"end_day"`
	DaysRemaining int                `json:"days_remaining"`
	Headline      string             `json:"headline"`
	VoterCount    int                `json:"voter_count"`
	TotalVotes    int                `json:"total_votes"`
	Totals        map[string]int     `json:"totals"`
	MyBallot      *civicBallot       `json:"my_ballot,omitempty"`
	LatestResult  *civicRoundSummary `json:"latest_result,omitempty"`
}

func NewPublicService(path string) (*PublicService, error) {
	service := &PublicService{path: path}
	raw, err := os.ReadFile(path)
	if err == nil {
		var stored publicStoredState
		if err := json.Unmarshal(raw, &stored); err != nil {
			return nil, fmt.Errorf("society public: decode: %w", err)
		}
		service.engine, err = model.Restore(stored.Checkpoint)
		if err != nil {
			return nil, err
		}
		service.civic, service.lastAdvanced = stored.Civic, stored.LastAdvanced
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("society public: read: %w", err)
	} else {
		cfg, err := model.DefaultConfig("C")
		if err != nil {
			return nil, err
		}
		cfg.Name = "公共 AI 社会"
		service.engine, err = model.New(cfg)
		if err != nil {
			return nil, err
		}
		service.engine.Resume()
		service.civic = civicState{Current: newRound(1, 0)}
		service.lastAdvanced = time.Now().UTC()
		if err := service.persist(); err != nil {
			return nil, err
		}
	}
	if service.civic.Current.ID == 0 {
		service.civic.Current = newRound(1, service.engine.Clock().Day)
	}
	if service.civic.Current.Ballots == nil {
		service.civic.Current.Ballots = map[string]civicBallot{}
	}
	if service.lastAdvanced.IsZero() {
		service.lastAdvanced = time.Now().UTC()
	}
	return service, nil
}

func (service *PublicService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var call publicCall
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&call); err != nil {
		writePublicError(w, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.advanceDue(time.Now().UTC()); err != nil {
		writePublicError(w, http.StatusInternalServerError, err)
		return
	}
	voterID := publicVoterID(w, r)
	var response any
	var err error
	switch call.Type {
	case "society.experiment.status", "society.experiment.snapshot":
		response = service.engine.StateSnapshot()
	case "society.experiment.issue":
		response = service.issue(voterID)
	case "society.experiment.population":
		response = publicPopulation(service.engine.StateSnapshot())
	case "society.experiment.network":
		response = publicNetwork(service.engine.StateSnapshot())
	case "society.experiment.history":
		var payload historyPayload
		err = decodePublic(call.Payload, &payload)
		if err == nil {
			response, err = service.engine.MetricsHistory(payload.FromDay, payload.ToDay, payload.Stride)
		}
	case "society.experiment.recent":
		var payload recentPayload
		err = decodePublic(call.Payload, &payload)
		if err == nil {
			response, err = service.engine.RecentHistory(payload.Limit)
		}
	case "society.experiment.trace":
		var payload tracePayload
		err = decodePublic(call.Payload, &payload)
		if err == nil {
			response, err = service.engine.SubjectTrace(payload.SubjectID, payload.Limit)
		}
	case "society.experiment.inspect":
		var payload inspectPayload
		err = decodePublic(call.Payload, &payload)
		if err == nil {
			var found bool
			response, found = publicInspect(service.engine.StateSnapshot(), payload.SubjectID)
			if !found {
				err = fmt.Errorf("citizen not found")
			}
		}
	case "society.experiment.vote":
		var payload votePayload
		err = decodePublic(call.Payload, &payload)
		if err == nil {
			err = validateVote(payload)
		}
		if err == nil {
			if _, exists := service.civic.Current.Ballots[voterID]; exists {
				err = fmt.Errorf("this browser already voted in the current issue")
			} else {
				service.civic.Current.Ballots[voterID] = civicBallot{PublicAI: payload.PublicAI, TransitionFund: payload.TransitionFund, AIDividend: payload.AIDividend, SubmittedDay: service.engine.Clock().Day}
				response = service.issue(voterID)
			}
		}
	default:
		err = fmt.Errorf("public society does not expose %q", call.Type)
	}
	if err != nil {
		writePublicError(w, http.StatusBadRequest, err)
		return
	}
	if err := service.persist(); err != nil {
		writePublicError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func newRound(id, day int) civicRound {
	return civicRound{ID: id, StartDay: day, EndDay: day + civicRoundDays - 1, Ballots: map[string]civicBallot{}}
}

func validateVote(payload votePayload) error {
	if payload.CommandID == "" || payload.PublicAI < 0 || payload.TransitionFund < 0 || payload.AIDividend < 0 || payload.PublicAI+payload.TransitionFund+payload.AIDividend != 10 {
		return fmt.Errorf("a command_id and exactly 10 non-negative budget votes are required")
	}
	return nil
}

func summarize(round civicRound) civicRoundSummary {
	totals := map[string]int{"public_ai": 0, "transition_fund": 0, "ai_dividend": 0}
	for _, ballot := range round.Ballots {
		totals["public_ai"] += ballot.PublicAI
		totals["transition_fund"] += ballot.TransitionFund
		totals["ai_dividend"] += ballot.AIDividend
	}
	return civicRoundSummary{ID: round.ID, StartDay: round.StartDay, EndDay: round.EndDay, ClosedDay: round.ClosedDay, VoterCount: len(round.Ballots), TotalVotes: totals["public_ai"] + totals["transition_fund"] + totals["ai_dividend"], Totals: totals, Winner: round.Winner, AppliedInterventionID: round.AppliedInterventionID}
}

func winner(totals map[string]int) string {
	if totals["public_ai"]+totals["transition_fund"]+totals["ai_dividend"] == 0 {
		return ""
	}
	choices := []string{"public_ai", "transition_fund", "ai_dividend"}
	sort.SliceStable(choices, func(i, j int) bool { return totals[choices[i]] > totals[choices[j]] })
	return choices[0]
}

func (service *PublicService) closeRound() error {
	round := &service.civic.Current
	summary := summarize(*round)
	round.ClosedDay, round.Winner = service.engine.Clock().Day, winner(summary.Totals)
	policy := service.engine.StateSnapshot().Government.Policy
	item := model.Intervention{ID: fmt.Sprintf("public-civic-%d-%s", round.ID, round.Winner), Day: round.ClosedDay}
	switch round.Winner {
	case "public_ai":
		value := min(policy.TransparencyBPS+1500, model.BasisPoints)
		item.TransparencyBPS = &value
	case "transition_fund":
		tax, benefit := min(policy.IncomeTaxBPS+300, model.BasisPoints), policy.DailyBenefit+250
		item.IncomeTaxBPS, item.DailyBenefit = &tax, &benefit
	case "ai_dividend":
		tax, benefit := min(policy.IncomeTaxBPS+600, model.BasisPoints), policy.DailyBenefit+500
		item.IncomeTaxBPS, item.DailyBenefit = &tax, &benefit
	}
	if round.Winner != "" {
		if _, err := service.engine.Intervene(item); err != nil {
			return err
		}
		round.AppliedInterventionID = item.ID
	}
	service.civic.History = append(service.civic.History, *round)
	service.civic.Current = newRound(round.ID+1, service.engine.Clock().Day)
	return nil
}

func (service *PublicService) issue(voterID string) civicIssue {
	round, day := service.civic.Current, service.engine.Clock().Day
	summary := summarize(round)
	issue := civicIssue{RoundID: round.ID, StartDay: round.StartDay, EndDay: round.EndDay, DaysRemaining: max(0, round.EndDay-day), Headline: "AI 创造的新财富，该归谁？", VoterCount: summary.VoterCount, TotalVotes: summary.TotalVotes, Totals: summary.Totals}
	if ballot, ok := round.Ballots[voterID]; ok {
		copy := ballot
		issue.MyBallot = &copy
	}
	if len(service.civic.History) > 0 {
		latest := summarize(service.civic.History[len(service.civic.History)-1])
		issue.LatestResult = &latest
	}
	return issue
}

func (service *PublicService) advanceDue(now time.Time) error {
	const simulatedDay = 30 * time.Second
	days := min(30, int(now.Sub(service.lastAdvanced)/simulatedDay))
	for range days {
		if service.engine.Clock().Stopped || service.engine.Clock().Day >= service.engine.Config().Days {
			break
		}
		if _, err := service.engine.Advance(); err != nil {
			return err
		}
		if service.engine.Clock().Day >= service.civic.Current.EndDay {
			if err := service.closeRound(); err != nil {
				return err
			}
		}
	}
	if days > 0 {
		service.lastAdvanced = service.lastAdvanced.Add(time.Duration(days) * simulatedDay)
	}
	return nil
}

func (service *PublicService) persist() error {
	raw, err := json.Marshal(publicStoredState{Checkpoint: service.engine.Checkpoint(), Civic: service.civic, LastAdvanced: service.lastAdvanced})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(service.path), 0o700); err != nil {
		return err
	}
	temporary := service.path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, service.path)
}

func publicPopulation(snapshot model.StateSnapshot) map[string]any {
	citizens := make([][]any, 0, len(snapshot.Citizens))
	for _, citizen := range snapshot.Citizens {
		citizens = append(citizens, []any{citizen.ID, citizen.Alive, citizen.EmployerID, snapshot.Balances[citizen.AccountID], citizen.HouseholdID, citizen.CommunityID})
	}
	firms := make([][]any, 0, len(snapshot.Firms))
	for _, firm := range snapshot.Firms {
		firms = append(firms, []any{firm.ID, firm.Active, len(firm.Employees), firm.Inventory, snapshot.Balances[firm.AccountID]})
	}
	return map[string]any{"day": snapshot.Clock.Day, "citizens": citizens, "firms": firms}
}

func publicNetwork(snapshot model.StateSnapshot) map[string]any {
	indexes := map[string]int{}
	for index, citizen := range snapshot.Citizens {
		indexes[citizen.ID] = index
	}
	edges := make([][]any, 0, len(snapshot.Relationships))
	for _, relation := range snapshot.Relationships {
		kind := 0
		if relation.Kind == "family" {
			kind = 1
		}
		edges = append(edges, []any{indexes[relation.A], indexes[relation.B], relation.TrustBPS, kind})
	}
	return map[string]any{"day": snapshot.Clock.Day, "edges": edges}
}

func publicInspect(snapshot model.StateSnapshot, subjectID string) (map[string]any, bool) {
	for _, citizen := range snapshot.Citizens {
		if citizen.ID != subjectID {
			continue
		}
		result := map[string]any{"citizen": citizen, "balance": snapshot.Balances[citizen.AccountID], "relationships": []model.Relationship{}}
		for _, household := range snapshot.Households {
			if household.ID == citizen.HouseholdID {
				result["household"] = household
				break
			}
		}
		relations := []model.Relationship{}
		for _, relation := range snapshot.Relationships {
			if relation.A == citizen.ID || relation.B == citizen.ID {
				relations = append(relations, relation)
			}
		}
		result["relationships"] = relations
		return result, true
	}
	return nil, false
}

func publicVoterID(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(publicVoterCookie); err == nil && len(cookie.Value) >= 16 && len(cookie.Value) <= 80 {
		return cookie.Value
	}
	id := uuid.NewString()
	http.SetCookie(w, &http.Cookie{Name: publicVoterCookie, Value: id, Path: "/", MaxAge: 365 * 24 * 60 * 60, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	return id
}

func decodePublic(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	return json.Unmarshal(raw, target)
}

func writePublicError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
