package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wanpengxie/atoll/drivers/tools/society/model"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "compare":
		err = compare(os.Args[2:])
	case "sweep":
		err = sweep(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "atoll-society:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage:
  atoll-society run     [--variant A] [--days 3650] [--seed 20260906] [--out DIR]
  atoll-society compare [--days 3650] [--seed 20260906] [--out DIR]
  atoll-society sweep   [--days 3650] [--seed-start 20260906] [--seeds 10] [--out DIR]`)
}

func sweep(args []string) error {
	set := flag.NewFlagSet("sweep", flag.ContinueOnError)
	days := set.Int("days", 10*model.DaysPerYear, "logical days per run")
	seedStart := set.Uint64("seed-start", model.DefaultSeed, "first deterministic seed")
	seedCount := set.Int("seeds", 10, "number of consecutive seeds")
	out := set.String("out", "society-output/sensitivity", "report directory")
	peakPressure := set.Int("peak-pressure-bps", -1, "override peak resource pressure, 0..10000")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *seedCount < 1 || *seedCount > 1000 {
		return fmt.Errorf("seeds must be in [1,1000]")
	}
	if *days < 1 {
		return fmt.Errorf("days must be positive")
	}
	if *peakPressure < -1 || *peakPressure > model.BasisPoints {
		return fmt.Errorf("peak-pressure-bps must be in [0,10000] or -1")
	}
	if *seedStart == 0 || uint64(*seedCount-1) > ^uint64(0)-*seedStart {
		return fmt.Errorf("seed range is invalid")
	}
	runs := make([]model.Summary, 0, *seedCount*4)
	for offset := 0; offset < *seedCount; offset++ {
		seed := *seedStart + uint64(offset)
		for _, variant := range []string{"A", "B", "C", "D"} {
			cfg, err := model.DefaultConfig(variant)
			if err != nil {
				return err
			}
			cfg.Days, cfg.Seed = *days, seed
			if *peakPressure >= 0 {
				cfg.Shock.PeakPressureBPS = *peakPressure
			}
			engine, err := model.New(cfg)
			if err != nil {
				return err
			}
			if err := engine.Run(*days); err != nil {
				return err
			}
			runs = append(runs, engine.Summary())
		}
	}
	report, err := model.NewSensitivityReport(*days, *seedStart, *seedCount, runs)
	if err != nil {
		return err
	}
	if err := model.ExportSensitivity(*out, report); err != nil {
		return err
	}
	return printJSON(report)
}

func run(args []string) error {
	set := flag.NewFlagSet("run", flag.ContinueOnError)
	variant := set.String("variant", "A", "institutional variant: A, B, C, or D")
	days := set.Int("days", 10*model.DaysPerYear, "logical days to run")
	seed := set.Uint64("seed", model.DefaultSeed, "deterministic random seed")
	out := set.String("out", "society-output/run", "export directory")
	if err := set.Parse(args); err != nil {
		return err
	}
	cfg, err := model.DefaultConfig(*variant)
	if err != nil {
		return err
	}
	cfg.Days, cfg.Seed = *days, *seed
	engine, err := model.New(cfg)
	if err != nil {
		return err
	}
	if err := engine.Run(*days); err != nil {
		return err
	}
	if err := engine.Export(*out); err != nil {
		return err
	}
	return printJSON(engine.Summary())
}

func compare(args []string) error {
	set := flag.NewFlagSet("compare", flag.ContinueOnError)
	days := set.Int("days", 10*model.DaysPerYear, "logical days to run")
	seed := set.Uint64("seed", model.DefaultSeed, "shared deterministic random seed")
	out := set.String("out", "society-output/comparison", "export directory")
	if err := set.Parse(args); err != nil {
		return err
	}
	comparison := model.Comparison{ModelVersion: model.ModelVersion, BuildVersion: model.BuildVersion, Seed: *seed, Days: *days}
	for _, variant := range []string{"A", "B", "C", "D"} {
		cfg, err := model.DefaultConfig(variant)
		if err != nil {
			return err
		}
		cfg.Days, cfg.Seed = *days, *seed
		engine, err := model.New(cfg)
		if err != nil {
			return err
		}
		if err := engine.Run(*days); err != nil {
			return err
		}
		runDir := filepath.Join(*out, "run-"+variant)
		if err := engine.Export(runDir); err != nil {
			return err
		}
		comparison.Runs = append(comparison.Runs, engine.Summary())
	}
	if err := model.ExportComparison(*out, comparison); err != nil {
		return err
	}
	return printJSON(comparison)
}

func printJSON(value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}
