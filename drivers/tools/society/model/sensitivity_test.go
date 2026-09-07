package model

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSensitivityReportAggregatesEveryVariant(t *testing.T) {
	var runs []Summary
	for seed := uint64(10); seed < 12; seed++ {
		for index, variant := range []string{"A", "B", "C", "D"} {
			runs = append(runs, Summary{
				ModelVersion: ModelVersion, Variant: variant, Seed: seed, Days: 30,
				Final: DailyMetrics{Population: 90 + index + int(seed-10)*2, MoneySupply: 1_000},
			})
		}
	}
	report, err := NewSensitivityReport(30, 10, 2, runs)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Variants) != 4 || report.Variants[0].Runs != 2 {
		t.Fatalf("report variants=%+v", report.Variants)
	}
	population := report.Variants[0].Metrics["population"]
	if population.Mean != 91 || population.Min != 90 || population.Max != 92 {
		t.Fatalf("population stats=%+v", population)
	}
}

func TestSensitivityReportRejectsIncompleteSeedMatrix(t *testing.T) {
	if _, err := NewSensitivityReport(30, 10, 2, []Summary{{Variant: "A"}}); err == nil {
		t.Fatal("incomplete variant/seed matrix was accepted")
	}
}

func TestExportSensitivityWritesMachineReadableArtifacts(t *testing.T) {
	var runs []Summary
	for _, variant := range []string{"A", "B", "C", "D"} {
		runs = append(runs, Summary{ModelVersion: ModelVersion, Variant: variant, Seed: 10, Days: 30})
	}
	report, err := NewSensitivityReport(30, 10, 1, runs)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := ExportSensitivity(dir, report); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sensitivity.json", "sensitivity-runs.csv", "sensitivity-summary.csv"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Size() == 0 {
			t.Fatalf("artifact %s info=%v err=%v", name, info, err)
		}
	}
}
