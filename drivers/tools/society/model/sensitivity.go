package model

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

var SensitivityMetricNames = []string{
	"population", "employment_rate", "total_output", "gini", "poverty_rate",
	"average_trust", "government_support", "polarization", "active_firms",
	"housing_secure_rate", "total_debt", "clustering_coefficient", "isolated_rate", "public_service",
	"recovery_days", "tax_collected", "benefits_paid", "treasury", "money_supply",
}

type MetricStats struct {
	Mean float64 `json:"mean"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
}

type VariantSensitivity struct {
	Variant string                 `json:"variant"`
	Runs    int                    `json:"runs"`
	Metrics map[string]MetricStats `json:"metrics"`
}

type SensitivityReport struct {
	ModelVersion string               `json:"model_version"`
	BuildVersion string               `json:"build_version"`
	Days         int                  `json:"days"`
	SeedStart    uint64               `json:"seed_start"`
	SeedCount    int                  `json:"seed_count"`
	Runs         []Summary            `json:"runs"`
	Variants     []VariantSensitivity `json:"variants"`
}

func NewSensitivityReport(days int, seedStart uint64, seedCount int, runs []Summary) (SensitivityReport, error) {
	if days < 1 || seedStart == 0 || seedCount < 1 {
		return SensitivityReport{}, fmt.Errorf("society: positive days, seed_start, and seed_count are required")
	}
	report := SensitivityReport{
		ModelVersion: ModelVersion, BuildVersion: BuildVersion, Days: days, SeedStart: seedStart, SeedCount: seedCount,
		Runs: append([]Summary(nil), runs...),
	}
	for _, variant := range []string{"A", "B", "C", "D"} {
		var selected []Summary
		for _, run := range runs {
			if run.Variant == variant {
				selected = append(selected, run)
			}
		}
		if len(selected) != seedCount {
			return SensitivityReport{}, fmt.Errorf("society: variant %s has %d runs, want %d", variant, len(selected), seedCount)
		}
		row := VariantSensitivity{Variant: variant, Runs: len(selected), Metrics: map[string]MetricStats{}}
		for _, name := range SensitivityMetricNames {
			stats := MetricStats{Min: metricValue(selected[0].Final, name), Max: metricValue(selected[0].Final, name)}
			for _, run := range selected {
				value := metricValue(run.Final, name)
				stats.Mean += value
				if value < stats.Min {
					stats.Min = value
				}
				if value > stats.Max {
					stats.Max = value
				}
			}
			stats.Mean /= float64(len(selected))
			row.Metrics[name] = stats
		}
		report.Variants = append(report.Variants, row)
	}
	return report, nil
}

func metricValue(m DailyMetrics, name string) float64 {
	switch name {
	case "population":
		return float64(m.Population)
	case "employment_rate":
		return m.EmploymentRate
	case "total_output":
		return float64(m.TotalOutput)
	case "gini":
		return m.Gini
	case "poverty_rate":
		return m.PovertyRate
	case "average_trust":
		return m.AverageTrust
	case "government_support":
		return m.GovernmentSupport
	case "polarization":
		return m.Polarization
	case "active_firms":
		return float64(m.ActiveFirms)
	case "housing_secure_rate":
		return m.HousingSecureRate
	case "total_debt":
		return float64(m.TotalDebt)
	case "clustering_coefficient":
		return m.ClusteringCoefficient
	case "isolated_rate":
		return m.IsolatedRate
	case "public_service":
		return m.PublicService
	case "recovery_days":
		return float64(m.RecoveryDays)
	case "tax_collected":
		return float64(m.TaxCollected)
	case "benefits_paid":
		return float64(m.BenefitsPaid)
	case "treasury":
		return float64(m.Treasury)
	case "money_supply":
		return float64(m.MoneySupply)
	default:
		panic("unknown sensitivity metric: " + name)
	}
}

func ExportSensitivity(dir string, report SensitivityReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "sensitivity.json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	if err := writeSensitivityRuns(filepath.Join(dir, "sensitivity-runs.csv"), report.Runs); err != nil {
		return err
	}
	return writeSensitivitySummary(filepath.Join(dir, "sensitivity-summary.csv"), report.Variants)
}

func writeSensitivityRuns(path string, runs []Summary) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	w := csv.NewWriter(file)
	header := []string{"variant", "seed", "days"}
	header = append(header, SensitivityMetricNames...)
	if err := w.Write(header); err != nil {
		return err
	}
	for _, run := range runs {
		row := []string{run.Variant, strconv.FormatUint(run.Seed, 10), strconv.Itoa(run.Days)}
		for _, name := range SensitivityMetricNames {
			row = append(row, strconv.FormatFloat(metricValue(run.Final, name), 'f', 8, 64))
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func writeSensitivitySummary(path string, variants []VariantSensitivity) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	w := csv.NewWriter(file)
	if err := w.Write([]string{"variant", "runs", "metric", "mean", "min", "max"}); err != nil {
		return err
	}
	for _, variant := range variants {
		for _, name := range SensitivityMetricNames {
			stats := variant.Metrics[name]
			if err := w.Write([]string{
				variant.Variant, strconv.Itoa(variant.Runs), name,
				strconv.FormatFloat(stats.Mean, 'f', 8, 64),
				strconv.FormatFloat(stats.Min, 'f', 8, 64),
				strconv.FormatFloat(stats.Max, 'f', 8, 64),
			}); err != nil {
				return err
			}
		}
	}
	w.Flush()
	return w.Error()
}
