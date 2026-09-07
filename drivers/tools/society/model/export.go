package model

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func (e *Engine) Export(dir string) error {
	if dir == "" {
		return fmt.Errorf("society: export directory is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("society: create export directory: %w", err)
	}
	if err := writeJSON(filepath.Join(dir, "snapshot.json"), e.StateSnapshot()); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "summary.json"), e.Summary()); err != nil {
		return err
	}
	if err := e.exportMetricsCSV(filepath.Join(dir, "metrics.csv")); err != nil {
		return err
	}
	if err := e.exportEventsCSV(filepath.Join(dir, "events.csv")); err != nil {
		return err
	}
	if err := e.exportTransactionsCSV(filepath.Join(dir, "transactions.csv")); err != nil {
		return err
	}
	return nil
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("society: encode %s: %w", path, err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("society: write %s: %w", path, err)
	}
	return nil
}

func (e *Engine) exportMetricsCSV(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("society: create metrics csv: %w", err)
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"model_version", "build_version", "experiment_id", "variant", "seed", "day", "population", "births", "deaths", "employment_rate", "total_output", "gini", "poverty_rate", "average_trust", "government_support", "polarization", "housing_secure_rate", "total_debt", "clustering_coefficient", "isolated_rate", "public_service", "recovery_days", "tax_collected", "benefits_paid", "active_firms", "treasury", "money_supply", "resource_pressure_bps", "perceived_pressure_bps", "average_food_price"})
	for _, m := range e.metrics {
		_ = w.Write([]string{
			ModelVersion, BuildVersion, e.experimentID, e.config.Variant, strconv.FormatUint(e.config.Seed, 10), strconv.Itoa(m.Day), strconv.Itoa(m.Population), strconv.Itoa(m.Births), strconv.Itoa(m.Deaths),
			formatFloat(m.EmploymentRate), strconv.Itoa(m.TotalOutput), formatFloat(m.Gini), formatFloat(m.PovertyRate),
			formatFloat(m.AverageTrust), formatFloat(m.GovernmentSupport), formatFloat(m.Polarization), formatFloat(m.HousingSecureRate), strconv.FormatInt(int64(m.TotalDebt), 10), formatFloat(m.ClusteringCoefficient), formatFloat(m.IsolatedRate), formatFloat(m.PublicService), strconv.Itoa(m.RecoveryDays), strconv.FormatInt(int64(m.TaxCollected), 10), strconv.FormatInt(int64(m.BenefitsPaid), 10), strconv.Itoa(m.ActiveFirms),
			strconv.FormatInt(int64(m.Treasury), 10), strconv.FormatInt(int64(m.MoneySupply), 10), strconv.Itoa(m.ResourcePressureBPS),
			strconv.Itoa(m.PerceivedPressureBPS), strconv.FormatInt(int64(m.AverageFoodPrice), 10),
		})
	}
	w.Flush()
	writeErr := w.Error()
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("society: write metrics csv: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("society: close metrics csv: %w", closeErr)
	}
	return nil
}

func (e *Engine) exportEventsCSV(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("society: create events csv: %w", err)
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"model_version", "build_version", "experiment_id", "event_id", "day", "type", "actors_json", "reason", "changes_json", "transaction_id"})
	for _, event := range e.events {
		actors, _ := json.Marshal(event.Actors)
		changes, _ := json.Marshal(event.Changes)
		_ = w.Write([]string{ModelVersion, BuildVersion, e.experimentID, event.ID, strconv.Itoa(event.Day), event.Type, string(actors), event.Reason, string(changes), event.TransactionID})
	}
	w.Flush()
	writeErr := w.Error()
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("society: write events csv: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("society: close events csv: %w", closeErr)
	}
	return nil
}

func (e *Engine) exportTransactionsCSV(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("society: create transactions csv: %w", err)
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"model_version", "build_version", "experiment_id", "transaction_id", "day", "kind", "from", "to", "amount", "status", "reason"})
	for _, tx := range e.ledger.transactions {
		_ = w.Write([]string{ModelVersion, BuildVersion, e.experimentID, tx.ID, strconv.Itoa(tx.Day), tx.Kind, tx.From, tx.To, strconv.FormatInt(int64(tx.Amount), 10), string(tx.Status), tx.Reason})
	}
	w.Flush()
	writeErr := w.Error()
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("society: write transactions csv: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("society: close transactions csv: %w", closeErr)
	}
	return nil
}

func formatFloat(value float64) string { return strconv.FormatFloat(value, 'f', 8, 64) }

type Comparison struct {
	ModelVersion string    `json:"model_version"`
	BuildVersion string    `json:"build_version"`
	Seed         uint64    `json:"seed"`
	Days         int       `json:"days"`
	Runs         []Summary `json:"runs"`
}

func ExportComparison(dir string, comparison Comparison) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "comparison.json"), comparison); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "comparison.csv"))
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"model_version", "build_version", "variant", "seed", "days", "population", "employment_rate", "gini", "poverty_rate", "average_trust", "government_support", "polarization", "active_firms", "treasury", "money_supply"})
	for _, run := range comparison.Runs {
		m := run.Final
		_ = w.Write([]string{ModelVersion, BuildVersion, run.Variant, strconv.FormatUint(run.Seed, 10), strconv.Itoa(run.Days), strconv.Itoa(m.Population), formatFloat(m.EmploymentRate), formatFloat(m.Gini), formatFloat(m.PovertyRate), formatFloat(m.AverageTrust), formatFloat(m.GovernmentSupport), formatFloat(m.Polarization), strconv.Itoa(m.ActiveFirms), strconv.FormatInt(int64(m.Treasury), 10), strconv.FormatInt(int64(m.MoneySupply), 10)})
	}
	w.Flush()
	writeErr, closeErr := w.Error(), f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
