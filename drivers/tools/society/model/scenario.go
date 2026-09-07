package model

import (
	"fmt"
	"strings"
)

const DefaultSeed uint64 = 20_260_906

func DefaultConfig(variant string) (Config, error) {
	variant = strings.ToUpper(strings.TrimSpace(variant))
	policy, ok := map[string]Policy{
		"A": {TransparencyBPS: 9_000, IncomeTaxBPS: 1_000, DailyBenefit: 400},
		"B": {TransparencyBPS: 3_000, IncomeTaxBPS: 1_000, DailyBenefit: 400},
		"C": {TransparencyBPS: 9_000, IncomeTaxBPS: 3_000, DailyBenefit: 1_200},
		"D": {TransparencyBPS: 3_000, IncomeTaxBPS: 3_000, DailyBenefit: 1_200},
	}[variant]
	if !ok {
		return Config{}, fmt.Errorf("society: unknown variant %q (want A, B, C, or D)", variant)
	}
	return Config{
		Name: "atoll-town-" + strings.ToLower(variant), Variant: variant,
		Seed: DefaultSeed, Population: 100, FirmCount: 10, Days: 10 * DaysPerYear,
		InitialEmploymentBPS: 7_500,
		InitialCitizenMoney:  50_000, InitialFirmMoney: 200_000, InitialTreasury: 2_000_000,
		// At 75% initial employment, 1,300 * .75 = 975: aggregate
		// payroll and staple demand begin in balance before policy transfers.
		DailyWage: 1_300, BaseFoodPrice: 975, DailyFoodNeed: 1, PovertyLine: 20_000,
		Policy: policy,
		Shock:  ShockConfig{StartDay: 3 * DaysPerYear, PeakDay: 6 * DaysPerYear, EndDay: 9 * DaysPerYear, PeakPressureBPS: 4_500},
	}, nil
}

func validateConfig(cfg Config) error {
	if cfg.Name == "" || cfg.Population < 1 || cfg.FirmCount < 1 || cfg.Days < 1 {
		return fmt.Errorf("society: name, positive population, firm_count, and days are required")
	}
	if cfg.InitialEmploymentBPS < 0 || cfg.InitialEmploymentBPS > BasisPoints ||
		cfg.Policy.TransparencyBPS < 0 || cfg.Policy.TransparencyBPS > BasisPoints ||
		cfg.Policy.IncomeTaxBPS < 0 || cfg.Policy.IncomeTaxBPS > BasisPoints {
		return fmt.Errorf("society: basis-point values must be in [0,%d]", BasisPoints)
	}
	if cfg.InitialCitizenMoney < 0 || cfg.InitialFirmMoney < 0 || cfg.InitialTreasury < 0 ||
		cfg.DailyWage <= 0 || cfg.BaseFoodPrice <= 0 || cfg.DailyFoodNeed <= 0 || cfg.PovertyLine < 0 || cfg.Policy.DailyBenefit < 0 {
		return fmt.Errorf("society: monetary and need values are invalid")
	}
	if cfg.Shock.StartDay < 0 || cfg.Shock.PeakDay <= cfg.Shock.StartDay || cfg.Shock.EndDay <= cfg.Shock.PeakDay || cfg.Shock.PeakPressureBPS < 0 || cfg.Shock.PeakPressureBPS > BasisPoints {
		return fmt.Errorf("society: shock configuration is invalid")
	}
	seen := map[string]struct{}{}
	for _, intervention := range cfg.Interventions {
		if intervention.ID == "" || intervention.Day < 1 {
			return fmt.Errorf("society: intervention id and positive day are required")
		}
		if _, duplicate := seen[intervention.ID]; duplicate {
			return fmt.Errorf("society: duplicate intervention id %q", intervention.ID)
		}
		seen[intervention.ID] = struct{}{}
	}
	return nil
}
