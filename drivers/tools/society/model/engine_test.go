package model

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func defaultEngine(t *testing.T, variant string) *Engine {
	t.Helper()
	cfg, err := DefaultConfig(variant)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestSeededRunsAreExactlyReproducible(t *testing.T) {
	a := defaultEngine(t, "A")
	b := defaultEngine(t, "A")
	if err := a.Run(365); err != nil {
		t.Fatal(err)
	}
	if err := b.Run(365); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Snapshot(), b.Snapshot()) {
		t.Fatal("same config and seed produced different snapshots")
	}
}

func TestVariantsShareInitialWorld(t *testing.T) {
	base := defaultEngine(t, "A").Snapshot()
	for _, variant := range []string{"B", "C", "D"} {
		got := defaultEngine(t, variant).Snapshot()
		if !reflect.DeepEqual(got.Citizens, base.Citizens) || !reflect.DeepEqual(got.Firms, base.Firms) ||
			!reflect.DeepEqual(got.Households, base.Households) || !reflect.DeepEqual(got.Relationships, base.Relationships) ||
			!reflect.DeepEqual(got.Balances, base.Balances) || got.InitialMoneySupply != base.InitialMoneySupply {
			t.Fatalf("variant %s changed initial world rather than policy only", variant)
		}
	}
}

func TestDailySettlementConservesMoney(t *testing.T) {
	engine := defaultEngine(t, "C")
	initial := engine.ledger.InitialTotal()
	for day := 1; day <= 730; day++ {
		metrics, err := engine.Advance()
		if err != nil {
			t.Fatal(err)
		}
		if metrics.MoneySupply != initial || engine.ledger.Total() != initial {
			t.Fatalf("day %d money=%d ledger=%d initial=%d", day, metrics.MoneySupply, engine.ledger.Total(), initial)
		}
	}
}

func TestEqualPriceMarketDoesNotConcentrateEverySaleInOneFirm(t *testing.T) {
	engine := defaultEngine(t, "A")
	if _, err := engine.Advance(); err != nil {
		t.Fatal(err)
	}
	sellers := map[string]struct{}{}
	for _, tx := range engine.ledger.transactions {
		if tx.Kind == "food" && tx.Status == TransactionCommitted {
			sellers[tx.To] = struct{}{}
		}
	}
	if len(sellers) < engine.config.FirmCount/2 {
		t.Fatalf("equal-price food sales reached only %d/%d firms", len(sellers), engine.config.FirmCount)
	}
}

func TestDefaultTownIsViableBeforeTheConfiguredShock(t *testing.T) {
	engine := defaultEngine(t, "A")
	if err := engine.Run(3 * DaysPerYear); err != nil {
		t.Fatal(err)
	}
	final := engine.Summary().Final
	if final.ResourcePressureBPS != 0 {
		t.Fatalf("pre-shock pressure=%d", final.ResourcePressureBPS)
	}
	if final.Population < 90 || final.ActiveFirms < 8 {
		t.Fatalf("default town collapsed before its external shock: population=%d firms=%d", final.Population, final.ActiveFirms)
	}
}

func TestSafetyNetIncludesEmployedResidentBelowPovertyLine(t *testing.T) {
	engine := defaultEngine(t, "C")
	var citizen *Citizen
	for i := range engine.citizens {
		if engine.citizens[i].EmployerID != "" {
			citizen = &engine.citizens[i]
			break
		}
	}
	if citizen == nil {
		t.Fatal("fixture has no employed resident")
	}
	before := engine.ledger.Balance(citizen.AccountID)
	move := before - engine.config.PovertyLine + 1
	if _, _, err := engine.ledger.Transfer("make-working-poor", 0, "test", citizen.AccountID, engine.government.AccountID, move); err != nil {
		t.Fatal(err)
	}
	poorBalance := engine.ledger.Balance(citizen.AccountID)
	engine.payBenefits()
	if got := engine.ledger.Balance(citizen.AccountID); got != poorBalance+engine.config.Policy.DailyBenefit {
		t.Fatalf("working-poor benefit balance=%d want %d", got, poorBalance+engine.config.Policy.DailyBenefit)
	}
}

func TestPauseManualStepResumeAndStop(t *testing.T) {
	engine := defaultEngine(t, "A")
	engine.Pause()
	if err := engine.Run(1); !errors.Is(err, ErrPaused) {
		t.Fatalf("paused run error=%v", err)
	}
	if engine.Clock().Day != 0 {
		t.Fatal("paused automatic run advanced time")
	}
	if _, err := engine.Advance(); err != nil {
		t.Fatalf("manual step while paused: %v", err)
	}
	if engine.Clock().Day != 1 {
		t.Fatalf("manual step day=%d", engine.Clock().Day)
	}
	engine.Resume()
	if err := engine.Run(2); err != nil {
		t.Fatal(err)
	}
	if engine.Clock().Day != 3 {
		t.Fatalf("resumed day=%d", engine.Clock().Day)
	}
	engine.Stop()
	if _, err := engine.Advance(); !errors.Is(err, ErrStopped) {
		t.Fatalf("step after stop error=%v", err)
	}
	if err := engine.Run(1); !errors.Is(err, ErrStopped) {
		t.Fatalf("run after stop error=%v", err)
	}
}

func TestScheduledInterventionAppliesOnce(t *testing.T) {
	cfg, _ := DefaultConfig("A")
	transparency, pressure := 5_500, 2_000
	cfg.Interventions = []Intervention{{ID: "policy-1", Day: 2, TransparencyBPS: &transparency, ResourcePressureBPS: &pressure}}
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Run(3); err != nil {
		t.Fatal(err)
	}
	snapshot := engine.Snapshot()
	if snapshot.Government.Policy.TransparencyBPS != transparency || snapshot.Metrics[1].ResourcePressureBPS != pressure {
		t.Fatalf("intervention not applied: %+v", snapshot.Government.Policy)
	}
	if !reflect.DeepEqual(snapshot.AppliedInterventions, []string{"policy-1"}) {
		t.Fatalf("applied=%v", snapshot.AppliedInterventions)
	}
	count := 0
	for _, event := range snapshot.Events {
		if event.Type == "society.shock.changed" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("intervention events=%d", count)
	}
}

func TestMetricsMatchWorldState(t *testing.T) {
	engine := defaultEngine(t, "B")
	metrics, err := engine.Advance()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := engine.Snapshot()
	alive, active := 0, 0
	for _, citizen := range snapshot.Citizens {
		if citizen.Alive {
			alive++
		}
	}
	for _, firm := range snapshot.Firms {
		if firm.Active {
			active++
		}
	}
	if metrics.Population != alive || metrics.ActiveFirms != active || metrics.Treasury != snapshot.Balances[snapshot.Government.AccountID] {
		t.Fatalf("metrics=%+v", metrics)
	}
	if metrics.TaxCollected <= 0 || metrics.BenefitsPaid <= 0 {
		t.Fatalf("daily fiscal flows were not measured: tax=%d benefits=%d", metrics.TaxCollected, metrics.BenefitsPaid)
	}
	for _, citizen := range snapshot.Citizens {
		if citizen.Alive && citizen.CurrentStrategy == "" {
			t.Fatalf("alive resident %s has no current strategy", citizen.ID)
		}
		if citizen.DailyIncome < 0 {
			t.Fatalf("resident %s has negative daily income", citizen.ID)
		}
	}
}

func TestMediaInformationEdgesReachEveryResident(t *testing.T) {
	engine := defaultEngine(t, "B")
	if len(engine.informationEdges) != engine.config.Population {
		t.Fatalf("information edges=%d want %d", len(engine.informationEdges), engine.config.Population)
	}
	if _, err := engine.Advance(); err != nil {
		t.Fatal(err)
	}
	for _, citizen := range engine.citizens {
		if citizen.Alive && citizen.InformationExposureBPS == 0 {
			t.Fatalf("resident %s received no information exposure", citizen.ID)
		}
	}
}

func TestExportContainsIdentityAndCompleteMetrics(t *testing.T) {
	engine := defaultEngine(t, "D")
	if err := engine.Run(30); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := engine.Export(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"snapshot.json", "summary.json", "metrics.csv", "events.csv", "transactions.csv"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Size() == 0 {
			t.Fatalf("export %s: info=%v err=%v", name, info, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var summary Summary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ModelVersion != ModelVersion || summary.Variant != "D" || summary.Days != 30 {
		t.Fatalf("summary=%+v", summary)
	}
	metricsCSV, err := os.ReadFile(filepath.Join(dir, "metrics.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(metricsCSV), "\n"); lines != 31 {
		t.Fatalf("metrics csv lines=%d want 31", lines)
	}
}

func TestCheckpointContinuationMatchesUninterruptedRun(t *testing.T) {
	uninterrupted := defaultEngine(t, "C")
	restarted := defaultEngine(t, "C")
	if err := uninterrupted.Run(300); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Run(200); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(restarted.Checkpoint())
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Run(100); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(uninterrupted.StateSnapshot(), restored.StateSnapshot()) {
		t.Fatal("checkpoint continuation diverged from uninterrupted run")
	}
	wantHistory, err := uninterrupted.MetricsHistory(1, 300, 7)
	if err != nil {
		t.Fatal(err)
	}
	gotHistory, err := restored.MetricsHistory(1, 300, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantHistory.Metrics, gotHistory.Metrics) {
		t.Fatal("checkpoint did not preserve metrics history")
	}
}

func TestMetricsHistoryRangesAndDownsamples(t *testing.T) {
	engine := defaultEngine(t, "B")
	if err := engine.Run(30); err != nil {
		t.Fatal(err)
	}
	history, err := engine.MetricsHistory(5, 20, 5)
	if err != nil {
		t.Fatal(err)
	}
	wantDays := []int{5, 10, 15, 20}
	if len(history.Metrics) != len(wantDays) {
		t.Fatalf("history points=%d want %d", len(history.Metrics), len(wantDays))
	}
	for i, metric := range history.Metrics {
		if metric.Day != wantDays[i] {
			t.Fatalf("history[%d].day=%d want %d", i, metric.Day, wantDays[i])
		}
	}
	if _, err := engine.MetricsHistory(1, 30, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.MetricsHistory(20, 5, 1); err == nil {
		t.Fatal("backwards history range was accepted")
	}
}

func TestCheckpointPreservesEventIdentityAndRecentSubjectTrace(t *testing.T) {
	uninterrupted := defaultEngine(t, "A")
	if err := uninterrupted.Run(12); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(uninterrupted.Checkpoint())
	if err != nil {
		t.Fatal(err)
	}
	leftEvents, leftTransactions := uninterrupted.HistoryCounts()
	rightEvents, rightTransactions := restored.HistoryCounts()
	if _, err := uninterrupted.Advance(); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Advance(); err != nil {
		t.Fatal(err)
	}
	left := uninterrupted.AuditSince(leftEvents, leftTransactions)
	right := restored.AuditSince(rightEvents, rightTransactions)
	if !reflect.DeepEqual(left, right) {
		t.Fatal("audit evidence changed across checkpoint restoration")
	}
	trace, err := restored.SubjectTrace("citizen-001", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Events) == 0 || len(trace.Transactions) == 0 {
		t.Fatalf("recent citizen trace=%+v", trace)
	}
}

func TestRestoreRejectsCorruptMoneySupply(t *testing.T) {
	checkpoint := defaultEngine(t, "A").Checkpoint()
	checkpoint.Balances[checkpoint.Government.AccountID]++
	if _, err := Restore(checkpoint); err == nil || !strings.Contains(err.Error(), "money supply") {
		t.Fatalf("restore error=%v", err)
	}
}

func TestAuditBundleContainsOnlyNewDeterministicEvidence(t *testing.T) {
	engine := defaultEngine(t, "A")
	events, transactions := engine.HistoryCounts()
	if _, err := engine.Advance(); err != nil {
		t.Fatal(err)
	}
	bundle := engine.AuditSince(events, transactions)
	if bundle.Day != 1 || bundle.ID == "" || bundle.Summary.Days != 1 || len(bundle.Events) == 0 || len(bundle.Transactions) == 0 {
		t.Fatalf("day-one audit bundle=%+v", bundle)
	}
	nextEvents, nextTransactions := engine.HistoryCounts()
	if _, err := engine.Advance(); err != nil {
		t.Fatal(err)
	}
	next := engine.AuditSince(nextEvents, nextTransactions)
	if next.Day != 2 || next.ID == bundle.ID {
		t.Fatalf("next audit id/day did not advance: first=%q next=%q day=%d", bundle.ID, next.ID, next.Day)
	}
}

func TestLifecycleBirthDeathMigrationAndBoundedMemory(t *testing.T) {
	birth := defaultEngine(t, "C")
	birth.clock.Day = DaysPerYear - 1
	for i := range birth.citizens {
		birth.citizens[i].HealthBPS = BasisPoints
		birth.citizens[i].HousingBPS = BasisPoints
	}
	before := len(birth.citizens)
	metrics, err := birth.Advance()
	if err != nil {
		t.Fatal(err)
	}
	if len(birth.citizens) != before+1 || metrics.Births != 1 {
		t.Fatalf("birth lifecycle citizens=%d births=%d", len(birth.citizens), metrics.Births)
	}
	if birth.ledger.Total() != birth.ledger.InitialTotal() {
		t.Fatal("zero-balance birth account changed money supply")
	}

	migration := defaultEngine(t, "A")
	migration.citizens[0].UnmetNeedDays = 5
	oldCommunity := migration.citizens[0].CommunityID
	migration.tryMigration()
	if migration.citizens[0].CommunityID == oldCommunity {
		t.Fatal("eligible resident did not migrate")
	}

	death := defaultEngine(t, "A")
	death.citizens[0].HealthBPS = 1
	death.citizens[0].FoodUnits = 0
	death.citizens[0].HousingBPS = 0
	death.citizens[0].AgeDays = 86 * DaysPerYear
	metrics, err = death.Advance()
	if err != nil {
		t.Fatal(err)
	}
	if death.citizens[0].Alive || metrics.Deaths != 1 {
		t.Fatalf("death lifecycle alive=%v deaths=%d", death.citizens[0].Alive, metrics.Deaths)
	}
	_, transactionOffset := death.HistoryCounts()
	if _, err := death.Advance(); err != nil {
		t.Fatal(err)
	}
	for _, tx := range death.ledger.transactions[transactionOffset:] {
		if tx.From == death.citizens[0].AccountID || tx.To == death.citizens[0].AccountID {
			t.Fatalf("dead resident acted in later transaction: %+v", tx)
		}
	}
	if death.citizens[0].CurrentStrategy != "inactive" {
		t.Fatalf("dead resident strategy=%q", death.citizens[0].CurrentStrategy)
	}

	for i := 0; i < 20; i++ {
		death.addEvent("test.memory", []string{"citizen-002"}, "bounded", nil, "")
	}
	if got := len(death.citizens[1].RecentExperiences); got != 12 {
		t.Fatalf("recent experience count=%d want 12", got)
	}
}

func TestEmergencyCreditDebtRepaymentAndNewMetrics(t *testing.T) {
	engine := defaultEngine(t, "C")
	citizen := &engine.citizens[0]
	initialSupply := engine.ledger.InitialTotal()
	initialBalance := engine.ledger.Balance(citizen.AccountID)
	if _, _, err := engine.ledger.Transfer("test-drain", 0, "test", citizen.AccountID, engine.government.AccountID, initialBalance); err != nil {
		t.Fatal(err)
	}
	beforeGovernment := engine.ledger.Balance(engine.government.AccountID)
	engine.extendEmergencyCredit(citizen, 500)
	if citizen.Debt != 500 || engine.ledger.Balance(citizen.AccountID) < 500 {
		t.Fatalf("credit was not recorded: debt=%d balance=%d", citizen.Debt, engine.ledger.Balance(citizen.AccountID))
	}
	if engine.ledger.Balance(engine.government.AccountID) != beforeGovernment-500 {
		t.Fatal("credit did not come from treasury")
	}
	engine.repayDebt(citizen, 100)
	if citizen.Debt != 400 || engine.ledger.Total() != initialSupply {
		t.Fatalf("repayment debt=%d supply=%d", citizen.Debt, engine.ledger.Total())
	}
	metrics, err := engine.Advance()
	if err != nil {
		t.Fatal(err)
	}
	if metrics.HousingSecureRate < 0 || metrics.HousingSecureRate > 1 || metrics.PublicService < 0 || metrics.PublicService > 1 || metrics.ClusteringCoefficient < 0 || metrics.ClusteringCoefficient > 1 || metrics.IsolatedRate < 0 || metrics.IsolatedRate > 1 {
		t.Fatalf("normalized metrics out of range: %+v", metrics)
	}
	if metrics.TotalDebt < 0 {
		t.Fatalf("negative total debt: %d", metrics.TotalDebt)
	}
}

func TestBrowserExportIncludesSnapshotMetricsAndRecentEvidence(t *testing.T) {
	engine := defaultEngine(t, "B")
	if err := engine.Run(20); err != nil {
		t.Fatal(err)
	}
	exported, err := engine.BrowserExport()
	if err != nil {
		t.Fatal(err)
	}
	if exported.Snapshot.BuildVersion == "" {
		t.Fatal("browser export omitted build identity")
	}
	if len(exported.Metrics.Metrics) != 20 || len(exported.Recent.Events) == 0 || len(exported.Recent.Transactions) == 0 {
		t.Fatalf("incomplete browser export: metrics=%d events=%d transactions=%d", len(exported.Metrics.Metrics), len(exported.Recent.Events), len(exported.Recent.Transactions))
	}
}

func TestShockRisesFallsAndRecoveryUsesAllFourThresholds(t *testing.T) {
	engine := defaultEngine(t, "A")
	engine.config.Shock = ShockConfig{StartDay: 10, PeakDay: 20, EndDay: 30, PeakPressureBPS: 8_000}
	want := map[int]int{10: 0, 15: 4_000, 20: 8_000, 25: 4_000, 30: 0}
	for day, pressure := range want {
		engine.clock.Day = day
		if got := engine.resourcePressure(); got != pressure {
			t.Fatalf("day %d pressure=%d want %d", day, got, pressure)
		}
	}
	baseline := DailyMetrics{Population: 100, EmploymentRate: .8, TotalOutput: 100, AverageTrust: .7}
	if recoveredTo(&baseline, DailyMetrics{Population: 90, EmploymentRate: .72, TotalOutput: 90, AverageTrust: .63}) != true {
		t.Fatal("exact 90% recovery thresholds were rejected")
	}
	if recoveredTo(&baseline, DailyMetrics{Population: 89, EmploymentRate: .8, TotalOutput: 100, AverageTrust: .7}) {
		t.Fatal("recovery ignored the population threshold")
	}
}
