package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type Engine struct {
	experimentID      string
	config            Config
	clock             Clock
	citizens          []Citizen
	households        []Household
	firms             []Firm
	government        Government
	media             Media
	relations         []Relationship
	informationEdges  []InformationEdge
	ledger            *Ledger
	metrics           []DailyMetrics
	events            []Event
	rng               stableRNG
	applied           map[string]struct{}
	pressureOverride  *int
	eventSequence     int
	birthsToday       int
	deathsToday       int
	preShockMetrics   *DailyMetrics
	recoveryDays      int
	taxCollectedToday Money
	benefitsPaidToday Money
}

func New(cfg Config) (*Engine, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(cfg)
	digest := sha256.Sum256(raw)
	e := &Engine{
		experimentID: cfg.Name + "-" + hex.EncodeToString(digest[:6]),
		config:       cfg, government: Government{AccountID: "government", Policy: cfg.Policy},
		ledger: newLedger(), rng: newStableRNG(cfg.Seed), applied: map[string]struct{}{}, recoveryDays: -1,
	}
	if err := e.initialize(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) initialize() error {
	if err := e.ledger.AddAccount(e.government.AccountID, e.config.InitialTreasury); err != nil {
		return err
	}
	for i := 0; i < e.config.FirmCount; i++ {
		id := fmt.Sprintf("firm-%03d", i+1)
		account := "firm:" + id
		if err := e.ledger.AddAccount(account, e.config.InitialFirmMoney); err != nil {
			return err
		}
		e.firms = append(e.firms, Firm{
			ID: id, AccountID: account, Active: true,
			ProductivityBPS: 24_000 + e.rng.signed(2_000), Wage: e.config.DailyWage,
			FoodPrice: e.config.BaseFoodPrice,
		})
	}

	householdSize := 2 + e.rng.intn(3)
	householdIndex := 1
	current := Household{ID: fmt.Sprintf("household-%03d", householdIndex)}
	for i := 0; i < e.config.Population; i++ {
		if len(current.Members) >= householdSize {
			e.households = append(e.households, current)
			householdIndex++
			householdSize = 2 + e.rng.intn(3)
			current = Household{ID: fmt.Sprintf("household-%03d", householdIndex)}
		}
		id := fmt.Sprintf("citizen-%03d", i+1)
		account := "citizen:" + id
		initial := e.config.InitialCitizenMoney + Money(e.rng.signed(int(e.config.InitialCitizenMoney/4)))
		if initial < 0 {
			initial = 0
		}
		if err := e.ledger.AddAccount(account, initial); err != nil {
			return err
		}
		citizen := Citizen{
			ID: id, AccountID: account, Alive: true, HouseholdID: current.ID,
			CommunityID: fmt.Sprintf("community-%02d", i%4+1), HousingBPS: 7_500 + e.rng.intn(2_001),
			CurrentStrategy: "seek-work",
			AgeDays:         (18*DaysPerYear + e.rng.intn(48*DaysPerYear)),
			HealthBPS:       8_000 + e.rng.intn(2_001), RiskBPS: 2_000 + e.rng.intn(6_001),
			FairnessBPS: 3_000 + e.rng.intn(6_001), GovernmentSupportBPS: 4_000 + e.rng.intn(2_001),
			Opinion: e.rng.signed(4_000), FoodUnits: e.config.DailyFoodNeed,
		}
		e.citizens = append(e.citizens, citizen)
		current.Members = append(current.Members, id)
	}
	if len(current.Members) > 0 {
		e.households = append(e.households, current)
	}

	employed := e.config.Population * e.config.InitialEmploymentBPS / BasisPoints
	order := make([]int, e.config.Population)
	for i := range order {
		order[i] = i
	}
	e.rng.shuffle(order)
	for position, citizenIndex := range order[:employed] {
		firmIndex := position % len(e.firms)
		e.citizens[citizenIndex].EmployerID = e.firms[firmIndex].ID
		e.citizens[citizenIndex].CurrentStrategy = "work-and-save"
		e.firms[firmIndex].Employees = append(e.firms[firmIndex].Employees, e.citizens[citizenIndex].ID)
	}
	for i := range e.firms {
		sort.Strings(e.firms[i].Employees)
	}
	e.initializeRelationships()
	e.initializeInformationEdges()
	e.ledger.Freeze()
	return nil
}

func (e *Engine) initializeInformationEdges() {
	for i := range e.citizens {
		e.informationEdges = append(e.informationEdges, InformationEdge{Source: "media", Target: e.citizens[i].ID, TrustBPS: 4_500 + e.citizens[i].FairnessBPS/4})
	}
}

func (e *Engine) initializeRelationships() {
	seen := map[string]struct{}{}
	add := func(a, b, kind string, trust int) {
		if a == b {
			return
		}
		if a > b {
			a, b = b, a
		}
		key := a + "\x00" + b
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		e.relations = append(e.relations, Relationship{A: a, B: b, Kind: kind, TrustBPS: trust})
	}
	for _, household := range e.households {
		for i := 0; i < len(household.Members); i++ {
			for j := i + 1; j < len(household.Members); j++ {
				add(household.Members[i], household.Members[j], "family", 7_000+e.rng.signed(500))
			}
		}
	}
	for i := range e.citizens {
		add(e.citizens[i].ID, e.citizens[(i+1)%len(e.citizens)].ID, "neighbor", 5_500+e.rng.signed(700))
		add(e.citizens[i].ID, e.citizens[(i+7)%len(e.citizens)].ID, "community", 5_000+e.rng.signed(800))
	}
	sort.Slice(e.relations, func(i, j int) bool {
		if e.relations[i].A == e.relations[j].A {
			return e.relations[i].B < e.relations[j].B
		}
		return e.relations[i].A < e.relations[j].A
	})
}

func (e *Engine) Pause() {
	if !e.clock.Stopped {
		e.clock.Paused = true
	}
}
func (e *Engine) Config() Config { return e.config }
func (e *Engine) Resume() {
	if !e.clock.Stopped {
		e.clock.Paused = false
	}
}
func (e *Engine) Stop()        { e.clock.Stopped = true; e.clock.Paused = true }
func (e *Engine) Clock() Clock { return e.clock }

// Run is the automatic runner face and respects Pause. Advance is the manual
// single-step face and may advance while paused.
func (e *Engine) Run(days int) error {
	if e.clock.Stopped {
		return ErrStopped
	}
	if e.clock.Paused {
		return ErrPaused
	}
	for i := 0; i < days; i++ {
		if _, err := e.Advance(); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) Advance() (DailyMetrics, error) {
	if e.clock.Stopped {
		return DailyMetrics{}, ErrStopped
	}
	e.clock.Day++
	e.birthsToday, e.deathsToday = 0, 0
	e.taxCollectedToday, e.benefitsPaidToday = 0, 0
	for i := range e.citizens {
		e.citizens[i].DailyIncome = 0
	}
	e.applyInterventions()
	pressure := e.resourcePressure()
	e.produce(pressure)
	e.payWages()
	e.payBenefits()
	e.purchaseFood(pressure)
	e.updateHousingAndServices()
	e.updateInformationNetwork()
	e.updateCitizens(pressure)
	e.updateRelationships()
	e.lifecycleAndMigration()
	if e.clock.Day%30 == 0 {
		e.rebalanceEmployment()
	}
	metrics := e.calculateMetrics(pressure)
	if e.clock.Day == e.config.Shock.StartDay {
		baseline := metrics
		e.preShockMetrics = &baseline
	}
	if e.recoveryDays < 0 && e.preShockMetrics != nil && e.clock.Day > e.config.Shock.PeakDay && pressure < e.config.Shock.PeakPressureBPS && recoveredTo(e.preShockMetrics, metrics) {
		e.recoveryDays = e.clock.Day - e.config.Shock.PeakDay
	}
	metrics.RecoveryDays = e.recoveryDays
	e.metrics = append(e.metrics, metrics)
	e.addEvent("society.day.completed", nil, "fixed daily settlement order completed", map[string]int{
		"population": metrics.Population, "active_firms": metrics.ActiveFirms, "total_output": metrics.TotalOutput,
	}, "")
	return metrics, nil
}

func (e *Engine) resourcePressure() int {
	if e.pressureOverride != nil {
		return clamp(*e.pressureOverride, 0, BasisPoints)
	}
	shock := e.config.Shock
	if e.clock.Day <= shock.StartDay {
		return 0
	}
	if e.clock.Day >= shock.EndDay {
		return 0
	}
	if e.clock.Day >= shock.PeakDay {
		return (shock.EndDay - e.clock.Day) * shock.PeakPressureBPS / (shock.EndDay - shock.PeakDay)
	}
	return (e.clock.Day - shock.StartDay) * shock.PeakPressureBPS / (shock.PeakDay - shock.StartDay)
}

func recoveredTo(baseline *DailyMetrics, current DailyMetrics) bool {
	if baseline == nil {
		return false
	}
	const epsilon = 1e-12
	return current.Population*10 >= baseline.Population*9 &&
		current.EmploymentRate+epsilon >= baseline.EmploymentRate*0.9 &&
		current.TotalOutput*10 >= baseline.TotalOutput*9 &&
		current.AverageTrust+epsilon >= baseline.AverageTrust*0.9
}

func (e *Engine) applyInterventions() {
	items := append([]Intervention(nil), e.config.Interventions...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Day == items[j].Day {
			return items[i].ID < items[j].ID
		}
		return items[i].Day < items[j].Day
	})
	for _, item := range items {
		if item.Day != e.clock.Day {
			continue
		}
		if _, exists := e.applied[item.ID]; exists {
			continue
		}
		e.applyIntervention(item, "scheduled intervention")
	}
}

// Intervene applies one idempotent runtime intervention immediately. Day may
// be zero (the current day) or equal the current day; future interventions
// belong in Config so their ordering is fixed before the run starts.
func (e *Engine) Intervene(item Intervention) (bool, error) {
	if item.ID == "" {
		return false, fmt.Errorf("society: intervention id is required")
	}
	if item.Day != 0 && item.Day != e.clock.Day {
		return false, fmt.Errorf("society: immediate intervention day %d does not match current day %d", item.Day, e.clock.Day)
	}
	if _, exists := e.applied[item.ID]; exists {
		return false, nil
	}
	item.Day = e.clock.Day
	e.applyIntervention(item, "runtime intervention")
	return true, nil
}

func (e *Engine) applyIntervention(item Intervention, reason string) {
	changes := map[string]int{}
	if item.TransparencyBPS != nil {
		e.government.Policy.TransparencyBPS = clamp(*item.TransparencyBPS, 0, BasisPoints)
		changes["transparency_bps"] = e.government.Policy.TransparencyBPS
	}
	if item.IncomeTaxBPS != nil {
		e.government.Policy.IncomeTaxBPS = clamp(*item.IncomeTaxBPS, 0, BasisPoints)
		changes["income_tax_bps"] = e.government.Policy.IncomeTaxBPS
	}
	if item.DailyBenefit != nil && *item.DailyBenefit >= 0 {
		e.government.Policy.DailyBenefit = *item.DailyBenefit
		changes["daily_benefit"] = int(*item.DailyBenefit)
	}
	if item.ResourcePressureBPS != nil {
		value := clamp(*item.ResourcePressureBPS, 0, BasisPoints)
		e.pressureOverride = &value
		changes["resource_pressure_bps"] = value
		e.addEvent("society.shock.changed", nil, reason, changes, "")
	} else {
		e.addEvent("society.policy.changed", nil, reason, changes, "")
	}
	e.applied[item.ID] = struct{}{}
}

func (e *Engine) produce(pressure int) {
	for i := range e.firms {
		firm := &e.firms[i]
		if !firm.Active {
			continue
		}
		output := len(firm.Employees) * firm.ProductivityBPS * (BasisPoints - pressure) / (BasisPoints * BasisPoints)
		if len(firm.Employees) > 0 && output < 1 {
			output = 1
		}
		firm.DailyOutput = output
		firm.Inventory += output
		firm.FoodPrice = e.config.BaseFoodPrice * Money(BasisPoints+pressure) / BasisPoints
		// Nominal wages follow the same cost-of-living index as the staple price.
		// Without this, a resource shock silently becomes a permanent real-wage
		// cut large enough to kill every employed resident, regardless of policy.
		firm.Wage = e.config.DailyWage * Money(BasisPoints+pressure) / BasisPoints
	}
}

func (e *Engine) payWages() {
	for fi := range e.firms {
		firm := &e.firms[fi]
		if !firm.Active {
			continue
		}
		employees := append([]string(nil), firm.Employees...)
		sort.Strings(employees)
		for _, citizenID := range employees {
			ci := e.citizenIndex(citizenID)
			if ci < 0 || !e.citizens[ci].Alive {
				continue
			}
			gross := firm.Wage
			if e.ledger.Balance(firm.AccountID) < gross {
				firm.LowLiquidityDays++
				e.endEmployment(citizenID, "firm could not meet daily payroll")
				continue
			}
			tax := gross * Money(e.government.Policy.IncomeTaxBPS) / BasisPoints
			net := gross - tax
			netID := fmt.Sprintf("d%06d-wage-net-%s", e.clock.Day, citizenID)
			taxID := fmt.Sprintf("d%06d-wage-tax-%s", e.clock.Day, citizenID)
			if net > 0 {
				if _, _, err := e.ledger.Transfer(netID, e.clock.Day, "wage", firm.AccountID, e.citizens[ci].AccountID, net); err != nil {
					continue
				}
				e.citizens[ci].DailyIncome += net
			}
			if tax > 0 {
				if _, _, err := e.ledger.Transfer(taxID, e.clock.Day, "income_tax", firm.AccountID, e.government.AccountID, tax); err != nil {
					continue
				}
				e.taxCollectedToday += tax
			}
			e.repayDebt(&e.citizens[ci], net/10)
			e.addEvent("society.wage.paid", []string{firm.ID, citizenID}, "employment contract", map[string]int{"gross": int(gross), "tax": int(tax), "net": int(net)}, netID)
		}
	}
}

func (e *Engine) payBenefits() {
	benefit := e.government.Policy.DailyBenefit
	if benefit <= 0 {
		return
	}
	for i := range e.citizens {
		citizen := &e.citizens[i]
		if !citizen.Alive || (citizen.EmployerID != "" && e.ledger.Balance(citizen.AccountID) >= e.config.PovertyLine) {
			continue
		}
		id := fmt.Sprintf("d%06d-benefit-%s", e.clock.Day, citizen.ID)
		if _, _, err := e.ledger.Transfer(id, e.clock.Day, "benefit", e.government.AccountID, citizen.AccountID, benefit); err != nil {
			continue
		}
		citizen.DailyIncome += benefit
		e.benefitsPaidToday += benefit
		reason := "eligible unemployed resident"
		if citizen.EmployerID != "" {
			reason = "employed resident below the configured poverty line"
		}
		e.addEvent("society.benefit.paid", []string{"government", citizen.ID}, reason, map[string]int{"amount": int(benefit)}, id)
	}
}

func (e *Engine) purchaseFood(pressure int) {
	for i := range e.citizens {
		citizen := &e.citizens[i]
		if !citizen.Alive {
			citizen.CurrentStrategy = "inactive"
			continue
		}
		need := e.config.DailyFoodNeed
		firmIndex := e.foodSeller(need, i)
		if firmIndex < 0 {
			citizen.UnmetNeedDays++
			citizen.LastDecisionReason = "no seller had sufficient inventory"
			e.addEvent("society.need.unmet", []string{citizen.ID}, citizen.LastDecisionReason, map[string]int{"food_shortfall": need}, "")
			continue
		}
		firm := &e.firms[firmIndex]
		cost := firm.FoodPrice * Money(need)
		e.shareHouseholdFoodBudget(citizen, cost)
		e.extendEmergencyCredit(citizen, cost)
		id := fmt.Sprintf("d%06d-food-%s", e.clock.Day, citizen.ID)
		if _, _, err := e.ledger.Transfer(id, e.clock.Day, "food", citizen.AccountID, firm.AccountID, cost); err != nil {
			citizen.UnmetNeedDays++
			citizen.LastDecisionReason = "available money was below the daily food cost"
			e.addEvent("society.need.unmet", []string{citizen.ID}, citizen.LastDecisionReason, map[string]int{"food_shortfall": need, "cost": int(cost)}, id)
			continue
		}
		firm.Inventory -= need
		citizen.FoodUnits += need
		citizen.UnmetNeedDays = 0
		citizen.LastDecisionReason = "purchased from the least expensive available seller"
		e.addEvent("society.food.purchased", []string{citizen.ID, firm.ID}, citizen.LastDecisionReason, map[string]int{"units": need, "cost": int(cost), "pressure_bps": pressure}, id)
	}
}

func (e *Engine) extendEmergencyCredit(citizen *Citizen, cost Money) {
	shortfall := cost - e.ledger.Balance(citizen.AccountID)
	if shortfall <= 0 || citizen.Debt >= e.config.PovertyLine || e.ledger.Balance(e.government.AccountID) < shortfall {
		return
	}
	id := fmt.Sprintf("d%06d-credit-%s", e.clock.Day, citizen.ID)
	if _, _, err := e.ledger.Transfer(id, e.clock.Day, "emergency_credit", e.government.AccountID, citizen.AccountID, shortfall); err != nil {
		return
	}
	citizen.Debt += shortfall
	e.addEvent("society.credit.extended", []string{"government", citizen.ID}, "basic-needs account lacked the staple-food price", map[string]int{"amount": int(shortfall), "debt": int(citizen.Debt)}, id)
}

func (e *Engine) repayDebt(citizen *Citizen, budget Money) {
	if citizen.Debt <= 0 || budget <= 0 {
		return
	}
	amount := budget
	if amount > citizen.Debt {
		amount = citizen.Debt
	}
	if amount > e.ledger.Balance(citizen.AccountID) {
		amount = e.ledger.Balance(citizen.AccountID)
	}
	if amount <= 0 {
		return
	}
	id := fmt.Sprintf("d%06d-debt-repay-%s", e.clock.Day, citizen.ID)
	if _, _, err := e.ledger.Transfer(id, e.clock.Day, "debt_repayment", citizen.AccountID, e.government.AccountID, amount); err != nil {
		return
	}
	citizen.Debt -= amount
	e.addEvent("society.credit.repaid", []string{citizen.ID, "government"}, "employed resident repaid one tenth of net daily wage", map[string]int{"amount": int(amount), "debt": int(citizen.Debt)}, id)
}

func (e *Engine) shareHouseholdFoodBudget(recipient *Citizen, cost Money) {
	shortfall := cost - e.ledger.Balance(recipient.AccountID)
	if shortfall <= 0 {
		return
	}
	var members []string
	for _, household := range e.households {
		if household.ID == recipient.HouseholdID {
			members = household.Members
			break
		}
	}
	for _, memberID := range members {
		if memberID == recipient.ID || shortfall <= 0 {
			continue
		}
		donorIndex := e.citizenIndex(memberID)
		if donorIndex < 0 || !e.citizens[donorIndex].Alive {
			continue
		}
		donor := &e.citizens[donorIndex]
		available := e.ledger.Balance(donor.AccountID) - cost
		if available <= 0 {
			continue
		}
		amount := available
		if amount > shortfall {
			amount = shortfall
		}
		id := fmt.Sprintf("d%06d-household-%s-%s", e.clock.Day, donor.ID, recipient.ID)
		if _, _, err := e.ledger.Transfer(id, e.clock.Day, "household_support", donor.AccountID, recipient.AccountID, amount); err != nil {
			continue
		}
		e.addEvent("society.household.supported", []string{donor.ID, recipient.ID}, "household member covered a staple-food shortfall", map[string]int{"amount": int(amount)}, id)
		shortfall -= amount
	}
}

func (e *Engine) foodSeller(need, buyerIndex int) int {
	bestPrice := Money(0)
	candidates := make([]int, 0, len(e.firms))
	for i := range e.firms {
		firm := &e.firms[i]
		if !firm.Active || firm.Inventory < need {
			continue
		}
		if len(candidates) == 0 || firm.FoodPrice < bestPrice {
			bestPrice = firm.FoodPrice
			candidates = candidates[:0]
			candidates = append(candidates, i)
		} else if firm.FoodPrice == bestPrice {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return -1
	}
	// Firms are stored in stable ID order. Rotating equal-price buyers across
	// that order models a neutral marketplace without introducing randomness
	// or making an arbitrary lexical tie-break an economic advantage.
	return candidates[(e.clock.Day+buyerIndex)%len(candidates)]
}

func (e *Engine) updateHousingAndServices() {
	treasuryRatio := int(e.ledger.Balance(e.government.AccountID) * BasisPoints / (e.config.InitialTreasury + 1))
	e.government.PublicServiceBPS = clamp(2_000+e.government.Policy.IncomeTaxBPS/2+treasuryRatio/3, 0, BasisPoints)
	for _, household := range e.households {
		var balance Money
		alive := 0
		for _, id := range household.Members {
			index := e.citizenIndex(id)
			if index < 0 || !e.citizens[index].Alive {
				continue
			}
			alive++
			balance += e.ledger.Balance(e.citizens[index].AccountID)
		}
		if alive == 0 {
			continue
		}
		target := 8_500
		if balance/Money(alive) < e.config.PovertyLine {
			target = 4_500
		}
		for _, id := range household.Members {
			index := e.citizenIndex(id)
			if index < 0 || !e.citizens[index].Alive {
				continue
			}
			if e.citizens[index].UnmetNeedDays > 0 {
				target = 3_000
			}
			e.citizens[index].HousingBPS = clamp((e.citizens[index].HousingBPS*29+target)/30, 0, BasisPoints)
		}
	}
}

func (e *Engine) updateCitizens(pressure int) {
	noiseSpan := (BasisPoints - e.government.Policy.TransparencyBPS) / 2
	e.media.PerceivedPressureBPS = clamp(pressure+e.rng.signed(noiseSpan), 0, BasisPoints)
	for i := range e.citizens {
		citizen := &e.citizens[i]
		if !citizen.Alive {
			continue
		}
		citizen.AgeDays++
		serviceRecovery := e.government.PublicServiceBPS / 2_500
		if citizen.FoodUnits >= e.config.DailyFoodNeed {
			citizen.FoodUnits -= e.config.DailyFoodNeed
			housingPenalty := 0
			if citizen.HousingBPS < 4_000 {
				housingPenalty = 4
			}
			citizen.HealthBPS = clamp(citizen.HealthBPS+8+serviceRecovery-housingPenalty, 0, BasisPoints)
		} else {
			citizen.HealthBPS = clamp(citizen.HealthBPS-10+serviceRecovery, 0, BasisPoints)
		}
		balance := e.ledger.Balance(citizen.AccountID)
		security := clamp(int(balance*BasisPoints/(e.config.PovertyLine*4+1)), 0, BasisPoints)
		fairnessEffect := 0
		if citizen.EmployerID == "" {
			fairnessEffect = int(e.government.Policy.DailyBenefit) * 2
		} else {
			fairnessEffect = -e.government.Policy.IncomeTaxBPS * citizen.FairnessBPS / BasisPoints
		}
		targetSupport := clamp(citizen.InformationExposureBPS/2+security/3-pressure/3+fairnessEffect/10+2_000, 0, BasisPoints)
		citizen.GovernmentSupportBPS = clamp((citizen.GovernmentSupportBPS*19+targetSupport)/20, 0, BasisPoints)
		// Opinion is a broad preference for stronger collective intervention
		// (positive) versus individual autonomy (negative). It changes slowly
		// from heterogeneous traits and lived conditions. Media error is a small
		// input, not an accumulating global drift; otherwise clipping perceived
		// pressure at zero makes every low-transparency society converge to one
		// artificial extreme.
		targetOpinion := citizen.FairnessBPS - BasisPoints/2 + pressure/3
		if citizen.EmployerID == "" {
			targetOpinion += 1_500
		}
		targetOpinion += (e.media.PerceivedPressureBPS - pressure) / 5
		citizen.Opinion = clamp((citizen.Opinion*49+targetOpinion)/50+e.rng.signed(8), -BasisPoints, BasisPoints)
		if citizen.HealthBPS == 0 || citizen.AgeDays > 85*DaysPerYear {
			e.endEmployment(citizen.ID, "resident lifecycle ended")
			citizen.Alive = false
			e.deathsToday++
			e.addEvent("society.citizen.died", []string{citizen.ID}, "health reached zero or age exceeded the simplified lifecycle", nil, "")
		}
		if !citizen.Alive {
			citizen.CurrentStrategy = "inactive"
		} else if citizen.AgeDays < 18*DaysPerYear {
			citizen.CurrentStrategy = "dependent"
		} else if citizen.UnmetNeedDays > 0 {
			citizen.CurrentStrategy = "secure-basic-needs"
		} else if citizen.Debt > 0 {
			citizen.CurrentStrategy = "repay-debt"
		} else if citizen.EmployerID == "" {
			citizen.CurrentStrategy = "seek-work"
		} else {
			citizen.CurrentStrategy = "work-and-save"
		}
	}
}

func (e *Engine) updateInformationNetwork() {
	for i := range e.informationEdges {
		edge := &e.informationEdges[i]
		edge.ReachBPS = clamp(e.government.Policy.TransparencyBPS*3/4+edge.TrustBPS/4, 0, BasisPoints)
		if index := e.citizenIndex(edge.Target); index >= 0 && e.citizens[index].Alive {
			e.citizens[index].InformationExposureBPS = edge.ReachBPS
		}
	}
}

func (e *Engine) lifecycleAndMigration() {
	if e.clock.Day%DaysPerYear == 0 {
		e.tryBirth()
	}
	if e.clock.Day%DaysPerYear == DaysPerYear/2 {
		e.tryMigration()
	}
}

func (e *Engine) tryBirth() {
	if len(e.citizens) >= e.config.Population+20 || len(e.households) == 0 {
		return
	}
	start := (e.clock.Day / DaysPerYear) % len(e.households)
	for offset := 0; offset < len(e.households); offset++ {
		householdIndex := (start + offset) % len(e.households)
		household := &e.households[householdIndex]
		var parent *Citizen
		for _, id := range household.Members {
			index := e.citizenIndex(id)
			if index >= 0 && e.citizens[index].Alive && e.citizens[index].AgeDays >= 22*DaysPerYear && e.citizens[index].AgeDays <= 45*DaysPerYear && e.citizens[index].HealthBPS >= 7_000 && e.citizens[index].HousingBPS >= 6_000 {
				parent = &e.citizens[index]
				break
			}
		}
		if parent == nil {
			continue
		}
		id := fmt.Sprintf("citizen-%03d", len(e.citizens)+1)
		account := "citizen:" + id
		if err := e.ledger.OpenZeroAccount(account); err != nil {
			return
		}
		parentID, communityID := parent.ID, parent.CommunityID
		housing, risk, fairness, support := parent.HousingBPS, parent.RiskBPS, parent.FairnessBPS, parent.GovernmentSupportBPS
		child := Citizen{ID: id, AccountID: account, Alive: true, HouseholdID: household.ID, CommunityID: communityID, HousingBPS: housing, HealthBPS: 9_000, RiskBPS: risk, FairnessBPS: fairness, GovernmentSupportBPS: support, CurrentStrategy: "dependent"}
		e.citizens = append(e.citizens, child)
		reach := clamp(e.government.Policy.TransparencyBPS*3/4+(4_500+fairness/4)/4, 0, BasisPoints)
		e.citizens[len(e.citizens)-1].InformationExposureBPS = reach
		e.informationEdges = append(e.informationEdges, InformationEdge{Source: "media", Target: id, ReachBPS: reach, TrustBPS: 4_500 + fairness/4})
		household.Members = append(household.Members, id)
		for _, member := range household.Members[:len(household.Members)-1] {
			e.relations = append(e.relations, Relationship{A: member, B: id, Kind: "family", TrustBPS: 7_500})
		}
		e.birthsToday++
		e.addEvent("society.citizen.born", []string{id, parentID}, "annual simplified lifecycle condition was met", nil, "")
		return
	}
}

func (e *Engine) tryMigration() {
	for i := range e.citizens {
		citizen := &e.citizens[i]
		if !citizen.Alive || citizen.AgeDays < 18*DaysPerYear || citizen.UnmetNeedDays == 0 {
			continue
		}
		old := citizen.CommunityID
		var number int
		_, _ = fmt.Sscanf(old, "community-%02d", &number)
		if number < 1 || number > 4 {
			number = 1
		}
		citizen.CommunityID = fmt.Sprintf("community-%02d", number%4+1)
		e.addEvent("society.citizen.migrated", []string{citizen.ID}, "persistent unmet need triggered a move to the next community", map[string]int{"from": number, "to": number%4 + 1}, "")
		return
	}
}

func (e *Engine) updateRelationships() {
	for i := range e.relations {
		relation := &e.relations[i]
		a, b := e.citizenIndex(relation.A), e.citizenIndex(relation.B)
		if a < 0 || b < 0 || !e.citizens[a].Alive || !e.citizens[b].Alive {
			continue
		}
		delta := 1
		if e.citizens[a].UnmetNeedDays > 0 || e.citizens[b].UnmetNeedDays > 0 {
			delta = -3
		}
		opinionDistance := e.citizens[a].Opinion - e.citizens[b].Opinion
		if opinionDistance < 0 {
			opinionDistance = -opinionDistance
		}
		if opinionDistance > 8_000 {
			delta--
		}
		relation.TrustBPS = clamp(relation.TrustBPS+delta, 0, BasisPoints)
	}
}

func (e *Engine) rebalanceEmployment() {
	for fi := range e.firms {
		firm := &e.firms[fi]
		if !firm.Active {
			continue
		}
		monthlyPayroll := firm.Wage * Money(len(firm.Employees)) * 30
		if len(firm.Employees) > 0 && e.ledger.Balance(firm.AccountID) < monthlyPayroll/3 {
			id := firm.Employees[len(firm.Employees)-1]
			e.endEmployment(id, "firm liquidity fell below one third of monthly payroll")
			firm.LowLiquidityDays++
			continue
		}
		// Hiring and layoffs use the same ten-day payroll reserve horizon.
		// Requiring a full extra month plus a large fixed cash buffer trapped
		// solvent firms in permanent under-employment after one lean period.
		nextTenDayPayroll := firm.Wage * Money(len(firm.Employees)+1) * 10
		if e.ledger.Balance(firm.AccountID) > nextTenDayPayroll {
			if ci := e.firstUnemployed(); ci >= 0 {
				citizen := &e.citizens[ci]
				citizen.EmployerID = firm.ID
				firm.Employees = append(firm.Employees, citizen.ID)
				sort.Strings(firm.Employees)
				e.addEvent("society.employment.started", []string{citizen.ID, firm.ID}, "firm had capacity and resident was first eligible candidate", nil, "")
			}
		}
		if len(firm.Employees) == 0 && e.ledger.Balance(firm.AccountID) < firm.Wage*10 {
			firm.Active = false
			e.addEvent("society.firm.closed", []string{firm.ID}, "no employees and liquidity below ten daily wages", nil, "")
		}
	}
}

func (e *Engine) endEmployment(citizenID, reason string) {
	ci := e.citizenIndex(citizenID)
	if ci < 0 || e.citizens[ci].EmployerID == "" {
		return
	}
	firmID := e.citizens[ci].EmployerID
	for fi := range e.firms {
		if e.firms[fi].ID != firmID {
			continue
		}
		for i, id := range e.firms[fi].Employees {
			if id == citizenID {
				e.firms[fi].Employees = append(e.firms[fi].Employees[:i], e.firms[fi].Employees[i+1:]...)
				break
			}
		}
		break
	}
	e.citizens[ci].EmployerID = ""
	e.addEvent("society.employment.ended", []string{citizenID, firmID}, reason, nil, "")
}

func (e *Engine) firstUnemployed() int {
	for i := range e.citizens {
		if e.citizens[i].Alive && e.citizens[i].EmployerID == "" {
			return i
		}
	}
	return -1
}

func (e *Engine) citizenIndex(id string) int {
	// IDs are sequential and the fallback keeps imported/custom scenarios safe.
	var n int
	if _, err := fmt.Sscanf(id, "citizen-%03d", &n); err == nil && n > 0 && n <= len(e.citizens) && e.citizens[n-1].ID == id {
		return n - 1
	}
	for i := range e.citizens {
		if e.citizens[i].ID == id {
			return i
		}
	}
	return -1
}

func (e *Engine) addEvent(kind string, actors []string, reason string, changes map[string]int, tx string) {
	e.eventSequence++
	id := fmt.Sprintf("d%06d-e%07d", e.clock.Day, e.eventSequence)
	e.events = append(e.events, Event{ID: id, Day: e.clock.Day, Type: kind, Actors: actors, Reason: reason, Changes: changes, TransactionID: tx})
	for _, actorID := range actors {
		index := e.citizenIndex(actorID)
		if index < 0 {
			continue
		}
		experiences := append(e.citizens[index].RecentExperiences, Experience{Day: e.clock.Day, Type: kind, Reason: reason})
		if len(experiences) > 12 {
			experiences = experiences[len(experiences)-12:]
		}
		e.citizens[index].RecentExperiences = experiences
	}
}

// HistoryCounts and AuditSince let runtime adapters turn each state-machine
// step into append-only evidence without exposing mutable engine internals.
func (e *Engine) HistoryCounts() (events, transactions int) {
	return len(e.events), len(e.ledger.transactions)
}

func (e *Engine) AuditSince(eventOffset, transactionOffset int) AuditBundle {
	if eventOffset < 0 || eventOffset > len(e.events) {
		eventOffset = len(e.events)
	}
	if transactionOffset < 0 || transactionOffset > len(e.ledger.transactions) {
		transactionOffset = len(e.ledger.transactions)
	}
	day := e.clock.Day
	events := append([]Event(nil), e.events[eventOffset:]...)
	transactions := append([]Transaction(nil), e.ledger.transactions[transactionOffset:]...)
	id := fmt.Sprintf("%s-day-%06d", e.experimentID, day)
	if len(events) > 0 {
		id += "-events-" + events[0].ID + "-through-" + events[len(events)-1].ID
	} else if len(transactions) > 0 {
		id += "-transactions-" + transactions[0].ID + "-through-" + transactions[len(transactions)-1].ID
	}
	return AuditBundle{
		ID: id, Day: day, Summary: e.Summary(), Events: events, Transactions: transactions,
	}
}

func (e *Engine) Summary() Summary {
	var final DailyMetrics
	if len(e.metrics) > 0 {
		final = e.metrics[len(e.metrics)-1]
	}
	var anomalies []string
	if final.Population < e.config.Population*3/4 {
		anomalies = append(anomalies, "population fell below 75% of the initial scenario")
	}
	if final.PovertyRate >= 0.95 {
		anomalies = append(anomalies, "cash-poverty rate reached at least 95%; inspect consumption and service indicators before interpreting welfare")
	}
	if final.ActiveFirms == 0 {
		anomalies = append(anomalies, "all firms exited")
	}
	if final.MoneySupply != e.ledger.InitialTotal() {
		anomalies = append(anomalies, "money-supply conservation failed")
	}
	return Summary{ModelVersion: ModelVersion, BuildVersion: BuildVersion, ExperimentID: e.experimentID, Variant: e.config.Variant, Seed: e.config.Seed, Days: e.clock.Day, Final: final, Anomalies: anomalies}
}

func (e *Engine) Snapshot() Snapshot {
	applied := make([]string, 0, len(e.applied))
	for id := range e.applied {
		applied = append(applied, id)
	}
	sort.Strings(applied)
	return Snapshot{
		ModelVersion: ModelVersion, BuildVersion: BuildVersion, ExperimentID: e.experimentID, Config: e.config, Clock: e.clock,
		Citizens: cloneCitizens(e.citizens), Households: cloneHouseholds(e.households),
		Firms: cloneFirms(e.firms), Government: e.government, Media: e.media,
		Relationships: append([]Relationship(nil), e.relations...), InformationEdges: append([]InformationEdge(nil), e.informationEdges...), Balances: e.ledger.balancesCopy(),
		InitialMoneySupply: e.ledger.InitialTotal(), Metrics: append([]DailyMetrics(nil), e.metrics...),
		Events: append([]Event(nil), e.events...), Transactions: e.ledger.transactionsCopy(), AppliedInterventions: applied,
	}
}

func (e *Engine) StateSnapshot() StateSnapshot {
	full := e.Snapshot()
	var latest *DailyMetrics
	if len(full.Metrics) > 0 {
		value := full.Metrics[len(full.Metrics)-1]
		latest = &value
	}
	return StateSnapshot{
		ModelVersion: full.ModelVersion, BuildVersion: full.BuildVersion, ExperimentID: full.ExperimentID, Config: full.Config, Clock: full.Clock,
		Citizens: full.Citizens, Households: full.Households, Firms: full.Firms,
		Government: full.Government, Media: full.Media, Relationships: full.Relationships, InformationEdges: full.InformationEdges,
		Balances: full.Balances, InitialMoneySupply: full.InitialMoneySupply,
		LatestMetrics: latest, AppliedInterventions: full.AppliedInterventions,
	}
}

func (e *Engine) Checkpoint() Checkpoint {
	state := e.StateSnapshot()
	var pressure *int
	if e.pressureOverride != nil {
		value := *e.pressureOverride
		pressure = &value
	}
	return Checkpoint{
		ModelVersion: state.ModelVersion, BuildVersion: state.BuildVersion, ExperimentID: state.ExperimentID, Config: state.Config, Clock: state.Clock,
		Citizens: state.Citizens, Households: state.Households, Firms: state.Firms,
		Government: state.Government, Media: state.Media, Relationships: state.Relationships, InformationEdges: state.InformationEdges,
		Balances: state.Balances, InitialMoneySupply: state.InitialMoneySupply,
		LatestMetrics: state.LatestMetrics, AppliedInterventions: state.AppliedInterventions,
		Metrics:      append([]DailyMetrics(nil), e.metrics...),
		RecentEvents: tailEvents(e.events, 1000), RecentTransactions: tailTransactions(e.ledger.transactions, 1000),
		RNGState: e.rng.state, EventSequence: e.eventSequence, PressureOverride: pressure,
		PreShockMetrics: e.preShockMetrics, RecoveryDays: e.recoveryDays,
	}
}

func Restore(checkpoint Checkpoint) (*Engine, error) {
	if checkpoint.ModelVersion != ModelVersion {
		return nil, fmt.Errorf("society: checkpoint model version %q, want %q", checkpoint.ModelVersion, ModelVersion)
	}
	if err := validateConfig(checkpoint.Config); err != nil {
		return nil, fmt.Errorf("society: checkpoint config: %w", err)
	}
	if checkpoint.ExperimentID == "" || checkpoint.Clock.Day < 0 || len(checkpoint.Citizens) < checkpoint.Config.Population || len(checkpoint.Citizens) > checkpoint.Config.Population+20 || len(checkpoint.Firms) != checkpoint.Config.FirmCount {
		return nil, fmt.Errorf("society: checkpoint shape is invalid")
	}
	ledger := newLedger()
	for id, balance := range checkpoint.Balances {
		if err := ledger.AddAccount(id, balance); err != nil {
			return nil, fmt.Errorf("society: checkpoint account %q: %w", id, err)
		}
	}
	ledger.Freeze()
	if ledger.InitialTotal() != checkpoint.InitialMoneySupply {
		return nil, fmt.Errorf("society: checkpoint money supply %d, want %d", ledger.InitialTotal(), checkpoint.InitialMoneySupply)
	}
	applied := make(map[string]struct{}, len(checkpoint.AppliedInterventions))
	for _, id := range checkpoint.AppliedInterventions {
		if id == "" {
			return nil, fmt.Errorf("society: checkpoint contains empty intervention id")
		}
		applied[id] = struct{}{}
	}
	e := &Engine{
		experimentID: checkpoint.ExperimentID, config: checkpoint.Config, clock: checkpoint.Clock,
		citizens: cloneCitizens(checkpoint.Citizens), households: cloneHouseholds(checkpoint.Households),
		firms: cloneFirms(checkpoint.Firms), government: checkpoint.Government, media: checkpoint.Media,
		relations: append([]Relationship(nil), checkpoint.Relationships...), informationEdges: append([]InformationEdge(nil), checkpoint.InformationEdges...), ledger: ledger,
		rng: stableRNG{state: checkpoint.RNGState}, applied: applied, eventSequence: checkpoint.EventSequence,
		recoveryDays: checkpoint.RecoveryDays,
	}
	e.events = append([]Event(nil), checkpoint.RecentEvents...)
	for _, tx := range checkpoint.RecentTransactions {
		e.ledger.seen[tx.ID] = len(e.ledger.transactions)
		e.ledger.transactions = append(e.ledger.transactions, tx)
	}
	if len(checkpoint.Metrics) > 0 {
		e.metrics = append([]DailyMetrics(nil), checkpoint.Metrics...)
	} else if checkpoint.LatestMetrics != nil {
		e.metrics = []DailyMetrics{*checkpoint.LatestMetrics}
	}
	if checkpoint.PressureOverride != nil {
		value := clamp(*checkpoint.PressureOverride, 0, BasisPoints)
		e.pressureOverride = &value
	}
	if checkpoint.PreShockMetrics != nil {
		value := *checkpoint.PreShockMetrics
		e.preShockMetrics = &value
	}
	return e, nil
}

func (e *Engine) SubjectTrace(subjectID string, limit int) (SubjectTrace, error) {
	if subjectID == "" || limit < 1 || limit > 500 {
		return SubjectTrace{}, fmt.Errorf("society: subject_id is required and limit must be in [1,500]")
	}
	accountID := subjectID
	if subjectID != "government" && !strings.Contains(subjectID, ":") {
		if strings.HasPrefix(subjectID, "citizen-") {
			accountID = "citizen:" + subjectID
		} else if strings.HasPrefix(subjectID, "firm-") {
			accountID = "firm:" + subjectID
		}
	}
	trace := SubjectTrace{ModelVersion: ModelVersion, ExperimentID: e.experimentID, SubjectID: subjectID}
	for i := len(e.events) - 1; i >= 0 && len(trace.Events) < limit; i-- {
		if containsString(e.events[i].Actors, subjectID) {
			trace.Events = append(trace.Events, e.events[i])
		}
	}
	for i := len(e.ledger.transactions) - 1; i >= 0 && len(trace.Transactions) < limit; i-- {
		tx := e.ledger.transactions[i]
		if tx.From == accountID || tx.To == accountID {
			trace.Transactions = append(trace.Transactions, tx)
		}
	}
	return trace, nil
}

func (e *Engine) RecentHistory(limit int) (RecentHistory, error) {
	if limit < 1 || limit > 500 {
		return RecentHistory{}, fmt.Errorf("society: limit must be in [1,500]")
	}
	return RecentHistory{
		ModelVersion: ModelVersion, ExperimentID: e.experimentID,
		Events: tailEvents(e.events, limit), Transactions: tailTransactions(e.ledger.transactions, limit),
	}, nil
}

func (e *Engine) BrowserExport() (BrowserExport, error) {
	stride := 1
	if e.clock.Day > 1000 {
		stride = (e.clock.Day + 999) / 1000
	}
	history, err := e.MetricsHistory(1, e.clock.Day, stride)
	if e.clock.Day == 0 {
		history = MetricsHistory{ModelVersion: ModelVersion, ExperimentID: e.experimentID, Variant: e.config.Variant, Seed: e.config.Seed, FromDay: 0, ToDay: 0, Stride: 1}
		err = nil
	}
	if err != nil {
		return BrowserExport{}, err
	}
	recent, err := e.RecentHistory(500)
	if err != nil {
		return BrowserExport{}, err
	}
	return BrowserExport{ModelVersion: ModelVersion, BuildVersion: BuildVersion, Snapshot: e.StateSnapshot(), Metrics: history, Recent: recent}, nil
}

func tailEvents(values []Event, limit int) []Event {
	if len(values) > limit {
		values = values[len(values)-limit:]
	}
	return append([]Event(nil), values...)
}

func tailTransactions(values []Transaction, limit int) []Transaction {
	if len(values) > limit {
		values = values[len(values)-limit:]
	}
	return append([]Transaction(nil), values...)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (e *Engine) MetricsHistory(fromDay, toDay, stride int) (MetricsHistory, error) {
	if fromDay == 0 {
		fromDay = 1
	}
	if toDay == 0 || toDay > e.clock.Day {
		toDay = e.clock.Day
	}
	if stride == 0 {
		stride = 1
	}
	if fromDay < 1 || toDay < fromDay || stride < 1 || stride > DaysPerYear {
		return MetricsHistory{}, fmt.Errorf("society: invalid history range or stride")
	}
	result := MetricsHistory{
		ModelVersion: ModelVersion, ExperimentID: e.experimentID, Variant: e.config.Variant, Seed: e.config.Seed,
		FromDay: fromDay, ToDay: toDay, Stride: stride,
	}
	for day := fromDay; day <= toDay; day += stride {
		index := day - 1
		if index >= 0 && index < len(e.metrics) && e.metrics[index].Day == day {
			result.Metrics = append(result.Metrics, e.metrics[index])
			continue
		}
		for _, metric := range e.metrics {
			if metric.Day == day {
				result.Metrics = append(result.Metrics, metric)
				break
			}
		}
	}
	if len(result.Metrics) > 1000 {
		return MetricsHistory{}, fmt.Errorf("society: history result exceeds 1000 points; increase stride")
	}
	return result, nil
}

func cloneHouseholds(in []Household) []Household {
	out := append([]Household(nil), in...)
	for i := range out {
		out[i].Members = append([]string(nil), out[i].Members...)
	}
	return out
}

func cloneCitizens(in []Citizen) []Citizen {
	out := append([]Citizen(nil), in...)
	for i := range out {
		out[i].RecentExperiences = append([]Experience(nil), out[i].RecentExperiences...)
	}
	return out
}

func cloneFirms(in []Firm) []Firm {
	out := append([]Firm(nil), in...)
	for i := range out {
		out[i].Employees = append([]string(nil), out[i].Employees...)
	}
	return out
}
