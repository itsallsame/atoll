// Package model implements the deterministic, domain-only state machine for
// Atoll's first digital-society experiment. It deliberately has no dependency
// on Atoll runtime packages: adapters may carry its commands and events over
// actors, but social rules remain independently testable and replayable.
package model

import "errors"

const ModelVersion = "digital-society-v1.1"

// BuildVersion is stamped by release builds. Source-tree runs intentionally
// report "dev" so exported evidence never masquerades as a published build.
var BuildVersion = "dev"

type Money int64

const (
	BasisPoints = 10_000
	DaysPerYear = 365
)

var (
	ErrPaused          = errors.New("society: experiment is paused")
	ErrStopped         = errors.New("society: experiment is stopped")
	ErrInsufficient    = errors.New("society: insufficient balance")
	ErrUnknownAccount  = errors.New("society: unknown account")
	ErrInvalidTransfer = errors.New("society: invalid transfer")
)

type Policy struct {
	TransparencyBPS int   `json:"transparency_bps"`
	IncomeTaxBPS    int   `json:"income_tax_bps"`
	DailyBenefit    Money `json:"daily_benefit"`
}

type ShockConfig struct {
	StartDay        int `json:"start_day"`
	PeakDay         int `json:"peak_day"`
	EndDay          int `json:"end_day"`
	PeakPressureBPS int `json:"peak_pressure_bps"`
}

type Intervention struct {
	ID                  string `json:"id"`
	Day                 int    `json:"day"`
	TransparencyBPS     *int   `json:"transparency_bps,omitempty"`
	IncomeTaxBPS        *int   `json:"income_tax_bps,omitempty"`
	DailyBenefit        *Money `json:"daily_benefit,omitempty"`
	ResourcePressureBPS *int   `json:"resource_pressure_bps,omitempty"`
}

type Config struct {
	Name                 string         `json:"name"`
	Variant              string         `json:"variant"`
	Seed                 uint64         `json:"seed"`
	Population           int            `json:"population"`
	FirmCount            int            `json:"firm_count"`
	Days                 int            `json:"days"`
	InitialEmploymentBPS int            `json:"initial_employment_bps"`
	InitialCitizenMoney  Money          `json:"initial_citizen_money"`
	InitialFirmMoney     Money          `json:"initial_firm_money"`
	InitialTreasury      Money          `json:"initial_treasury"`
	DailyWage            Money          `json:"daily_wage"`
	BaseFoodPrice        Money          `json:"base_food_price"`
	DailyFoodNeed        int            `json:"daily_food_need"`
	PovertyLine          Money          `json:"poverty_line"`
	Policy               Policy         `json:"policy"`
	Shock                ShockConfig    `json:"shock"`
	Interventions        []Intervention `json:"interventions,omitempty"`
}

type Clock struct {
	Day     int  `json:"day"`
	Paused  bool `json:"paused"`
	Stopped bool `json:"stopped"`
}

type Citizen struct {
	ID                     string       `json:"id"`
	AccountID              string       `json:"account_id"`
	AgeDays                int          `json:"age_days"`
	Alive                  bool         `json:"alive"`
	HouseholdID            string       `json:"household_id"`
	EmployerID             string       `json:"employer_id,omitempty"`
	CommunityID            string       `json:"community_id"`
	Debt                   Money        `json:"debt"`
	DailyIncome            Money        `json:"daily_income"`
	HousingBPS             int          `json:"housing_bps"`
	HealthBPS              int          `json:"health_bps"`
	RiskBPS                int          `json:"risk_bps"`
	FairnessBPS            int          `json:"fairness_bps"`
	GovernmentSupportBPS   int          `json:"government_support_bps"`
	Opinion                int          `json:"opinion"`
	FoodUnits              int          `json:"food_units"`
	UnmetNeedDays          int          `json:"unmet_need_days"`
	LastDecisionReason     string       `json:"last_decision_reason,omitempty"`
	CurrentStrategy        string       `json:"current_strategy"`
	RecentExperiences      []Experience `json:"recent_experiences,omitempty"`
	InformationExposureBPS int          `json:"information_exposure_bps"`
}

type Experience struct {
	Day    int    `json:"day"`
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
}

type Household struct {
	ID      string   `json:"id"`
	Members []string `json:"members"`
}

type Firm struct {
	ID               string   `json:"id"`
	AccountID        string   `json:"account_id"`
	Active           bool     `json:"active"`
	Employees        []string `json:"employees"`
	Inventory        int      `json:"inventory"`
	DailyOutput      int      `json:"daily_output"`
	ProductivityBPS  int      `json:"productivity_bps"`
	Wage             Money    `json:"wage"`
	FoodPrice        Money    `json:"food_price"`
	LowLiquidityDays int      `json:"low_liquidity_days"`
}

type Government struct {
	AccountID        string `json:"account_id"`
	Policy           Policy `json:"policy"`
	PublicServiceBPS int    `json:"public_service_bps"`
}

type Media struct {
	PerceivedPressureBPS int `json:"perceived_pressure_bps"`
}

type Relationship struct {
	A        string `json:"a"`
	B        string `json:"b"`
	TrustBPS int    `json:"trust_bps"`
	Kind     string `json:"kind"`
}

type InformationEdge struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReachBPS int    `json:"reach_bps"`
	TrustBPS int    `json:"trust_bps"`
}

type Event struct {
	ID            string         `json:"id"`
	Day           int            `json:"day"`
	Type          string         `json:"type"`
	Actors        []string       `json:"actors,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	Changes       map[string]int `json:"changes,omitempty"`
	TransactionID string         `json:"transaction_id,omitempty"`
}

type DailyMetrics struct {
	Day                   int     `json:"day"`
	Population            int     `json:"population"`
	EmploymentRate        float64 `json:"employment_rate"`
	TotalOutput           int     `json:"total_output"`
	Gini                  float64 `json:"gini"`
	PovertyRate           float64 `json:"poverty_rate"`
	AverageTrust          float64 `json:"average_trust"`
	GovernmentSupport     float64 `json:"government_support"`
	Polarization          float64 `json:"polarization"`
	ActiveFirms           int     `json:"active_firms"`
	Treasury              Money   `json:"treasury"`
	MoneySupply           Money   `json:"money_supply"`
	ResourcePressureBPS   int     `json:"resource_pressure_bps"`
	PerceivedPressureBPS  int     `json:"perceived_pressure_bps"`
	AverageFoodPrice      Money   `json:"average_food_price"`
	Births                int     `json:"births"`
	Deaths                int     `json:"deaths"`
	HousingSecureRate     float64 `json:"housing_secure_rate"`
	TotalDebt             Money   `json:"total_debt"`
	ClusteringCoefficient float64 `json:"clustering_coefficient"`
	IsolatedRate          float64 `json:"isolated_rate"`
	PublicService         float64 `json:"public_service"`
	RecoveryDays          int     `json:"recovery_days"`
	TaxCollected          Money   `json:"tax_collected"`
	BenefitsPaid          Money   `json:"benefits_paid"`
}

type Snapshot struct {
	ModelVersion         string            `json:"model_version"`
	BuildVersion         string            `json:"build_version"`
	ExperimentID         string            `json:"experiment_id"`
	Config               Config            `json:"config"`
	Clock                Clock             `json:"clock"`
	Citizens             []Citizen         `json:"citizens"`
	Households           []Household       `json:"households"`
	Firms                []Firm            `json:"firms"`
	Government           Government        `json:"government"`
	Media                Media             `json:"media"`
	Relationships        []Relationship    `json:"relationships"`
	InformationEdges     []InformationEdge `json:"information_edges"`
	Balances             map[string]Money  `json:"balances"`
	InitialMoneySupply   Money             `json:"initial_money_supply"`
	Metrics              []DailyMetrics    `json:"metrics"`
	Events               []Event           `json:"events"`
	Transactions         []Transaction     `json:"transactions"`
	AppliedInterventions []string          `json:"applied_interventions"`
}

// StateSnapshot is the compact current-state artifact written by Export.
// Append histories have their own CSV datasets; embedding them again in the
// state file makes a ten-year run needlessly duplicate hundreds of megabytes.
type StateSnapshot struct {
	ModelVersion         string            `json:"model_version"`
	BuildVersion         string            `json:"build_version"`
	ExperimentID         string            `json:"experiment_id"`
	Config               Config            `json:"config"`
	Clock                Clock             `json:"clock"`
	Citizens             []Citizen         `json:"citizens"`
	Households           []Household       `json:"households"`
	Firms                []Firm            `json:"firms"`
	Government           Government        `json:"government"`
	Media                Media             `json:"media"`
	Relationships        []Relationship    `json:"relationships"`
	InformationEdges     []InformationEdge `json:"information_edges"`
	Balances             map[string]Money  `json:"balances"`
	InitialMoneySupply   Money             `json:"initial_money_supply"`
	LatestMetrics        *DailyMetrics     `json:"latest_metrics,omitempty"`
	AppliedInterventions []string          `json:"applied_interventions"`
}

// Checkpoint is the compact durable continuation state. Histories are omitted:
// when hosted by Atoll they live in the channel ledger, while the standalone
// runner exports them as append datasets. RNGState is included so continuation
// after a process replacement remains bit-for-bit deterministic.
type Checkpoint struct {
	ModelVersion         string            `json:"model_version"`
	BuildVersion         string            `json:"build_version"`
	ExperimentID         string            `json:"experiment_id"`
	Config               Config            `json:"config"`
	Clock                Clock             `json:"clock"`
	Citizens             []Citizen         `json:"citizens"`
	Households           []Household       `json:"households"`
	Firms                []Firm            `json:"firms"`
	Government           Government        `json:"government"`
	Media                Media             `json:"media"`
	Relationships        []Relationship    `json:"relationships"`
	InformationEdges     []InformationEdge `json:"information_edges"`
	Balances             map[string]Money  `json:"balances"`
	InitialMoneySupply   Money             `json:"initial_money_supply"`
	LatestMetrics        *DailyMetrics     `json:"latest_metrics,omitempty"`
	Metrics              []DailyMetrics    `json:"metrics,omitempty"`
	RecentEvents         []Event           `json:"recent_events,omitempty"`
	RecentTransactions   []Transaction     `json:"recent_transactions,omitempty"`
	AppliedInterventions []string          `json:"applied_interventions"`
	RNGState             uint64            `json:"rng_state"`
	EventSequence        int               `json:"event_sequence"`
	PressureOverride     *int              `json:"pressure_override_bps,omitempty"`
	PreShockMetrics      *DailyMetrics     `json:"pre_shock_metrics,omitempty"`
	RecoveryDays         int               `json:"recovery_days"`
}

type MetricsHistory struct {
	ModelVersion string         `json:"model_version"`
	ExperimentID string         `json:"experiment_id"`
	Variant      string         `json:"variant"`
	Seed         uint64         `json:"seed"`
	FromDay      int            `json:"from_day"`
	ToDay        int            `json:"to_day"`
	Stride       int            `json:"stride"`
	Metrics      []DailyMetrics `json:"metrics"`
}

type SubjectTrace struct {
	ModelVersion string        `json:"model_version"`
	ExperimentID string        `json:"experiment_id"`
	SubjectID    string        `json:"subject_id"`
	Events       []Event       `json:"events"`
	Transactions []Transaction `json:"transactions"`
}

type RecentHistory struct {
	ModelVersion string        `json:"model_version"`
	ExperimentID string        `json:"experiment_id"`
	Events       []Event       `json:"events"`
	Transactions []Transaction `json:"transactions"`
}

type BrowserExport struct {
	ModelVersion string         `json:"model_version"`
	BuildVersion string         `json:"build_version"`
	Snapshot     StateSnapshot  `json:"snapshot"`
	Metrics      MetricsHistory `json:"metrics"`
	Recent       RecentHistory  `json:"recent"`
}

type Summary struct {
	ModelVersion string       `json:"model_version"`
	BuildVersion string       `json:"build_version"`
	ExperimentID string       `json:"experiment_id"`
	Variant      string       `json:"variant"`
	Seed         uint64       `json:"seed"`
	Days         int          `json:"days"`
	Final        DailyMetrics `json:"final"`
	Anomalies    []string     `json:"anomalies,omitempty"`
}

// AuditBundle is the append-only evidence produced by one logical day. Its ID
// is deterministic, so an Atoll adapter may safely redeliver it after a crash
// and downstream consumers can deduplicate without interpreting the payload.
type AuditBundle struct {
	ID           string        `json:"id"`
	Day          int           `json:"day"`
	Summary      Summary       `json:"summary"`
	Events       []Event       `json:"events"`
	Transactions []Transaction `json:"transactions"`
}

func clamp(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}
