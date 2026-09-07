package model

import "sort"

func (e *Engine) calculateMetrics(pressure int) DailyMetrics {
	wealth := make([]Money, 0, len(e.citizens))
	opinions := make([]int, 0, len(e.citizens))
	alive, working, labor, poor, housingSecure := 0, 0, 0, 0, 0
	var totalDebt Money
	var support int64
	for _, citizen := range e.citizens {
		if !citizen.Alive {
			continue
		}
		alive++
		balance := e.ledger.Balance(citizen.AccountID)
		wealth = append(wealth, balance)
		opinions = append(opinions, citizen.Opinion)
		support += int64(citizen.GovernmentSupportBPS)
		totalDebt += citizen.Debt
		if citizen.HousingBPS >= 6_000 {
			housingSecure++
		}
		if citizen.AgeDays >= 18*DaysPerYear && citizen.AgeDays <= 70*DaysPerYear {
			labor++
			if citizen.EmployerID != "" {
				working++
			}
		}
		if balance < e.config.PovertyLine {
			poor++
		}
	}
	activeFirms, output := 0, 0
	var priceTotal Money
	for _, firm := range e.firms {
		if !firm.Active {
			continue
		}
		activeFirms++
		output += firm.DailyOutput
		priceTotal += firm.FoodPrice
	}
	var trustTotal int64
	trustCount := 0
	for _, relation := range e.relations {
		a, b := e.citizenIndex(relation.A), e.citizenIndex(relation.B)
		if a >= 0 && b >= 0 && e.citizens[a].Alive && e.citizens[b].Alive {
			trustTotal += int64(relation.TrustBPS)
			trustCount++
		}
	}
	metrics := DailyMetrics{
		Day: e.clock.Day, Population: alive, TotalOutput: output, Gini: gini(wealth),
		ActiveFirms: activeFirms, Treasury: e.ledger.Balance(e.government.AccountID),
		MoneySupply: e.ledger.Total(), ResourcePressureBPS: pressure,
		PerceivedPressureBPS: e.media.PerceivedPressureBPS, Polarization: polarization(opinions),
		Births: e.birthsToday, Deaths: e.deathsToday, TotalDebt: totalDebt,
		ClusteringCoefficient: clusteringCoefficient(e.citizens, e.relations),
		IsolatedRate:          isolatedRate(e.citizens, e.relations),
		PublicService:         float64(e.government.PublicServiceBPS) / BasisPoints,
		TaxCollected:          e.taxCollectedToday,
		BenefitsPaid:          e.benefitsPaidToday,
	}
	if labor > 0 {
		metrics.EmploymentRate = float64(working) / float64(labor)
	}
	if alive > 0 {
		metrics.PovertyRate = float64(poor) / float64(alive)
		metrics.GovernmentSupport = float64(support) / float64(alive*BasisPoints)
		metrics.HousingSecureRate = float64(housingSecure) / float64(alive)
	}
	if trustCount > 0 {
		metrics.AverageTrust = float64(trustTotal) / float64(trustCount*BasisPoints)
	}
	if activeFirms > 0 {
		metrics.AverageFoodPrice = priceTotal / Money(activeFirms)
	}
	return metrics
}

func aliveAdjacency(citizens []Citizen, relations []Relationship) (map[string]map[string]struct{}, int) {
	alive := make(map[string]bool, len(citizens))
	adj := make(map[string]map[string]struct{}, len(citizens))
	for _, citizen := range citizens {
		if citizen.Alive {
			alive[citizen.ID] = true
			adj[citizen.ID] = make(map[string]struct{})
		}
	}
	for _, relation := range relations {
		if relation.TrustBPS <= 0 || !alive[relation.A] || !alive[relation.B] || relation.A == relation.B {
			continue
		}
		adj[relation.A][relation.B] = struct{}{}
		adj[relation.B][relation.A] = struct{}{}
	}
	return adj, len(alive)
}

func isolatedRate(citizens []Citizen, relations []Relationship) float64 {
	adj, alive := aliveAdjacency(citizens, relations)
	if alive == 0 {
		return 0
	}
	isolated := 0
	for _, citizen := range citizens {
		if !citizen.Alive {
			continue
		}
		neighbors := adj[citizen.ID]
		if len(neighbors) == 0 {
			isolated++
		}
	}
	return float64(isolated) / float64(alive)
}

func clusteringCoefficient(citizens []Citizen, relations []Relationship) float64 {
	adj, _ := aliveAdjacency(citizens, relations)
	var sum float64
	eligible := 0
	for _, citizen := range citizens {
		if !citizen.Alive {
			continue
		}
		neighbors := adj[citizen.ID]
		if len(neighbors) < 2 {
			continue
		}
		ids := make([]string, 0, len(neighbors))
		for id := range neighbors {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		links := 0
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				if _, ok := adj[ids[i]][ids[j]]; ok {
					links++
				}
			}
		}
		possible := len(ids) * (len(ids) - 1) / 2
		sum += float64(links) / float64(possible)
		eligible++
	}
	if eligible == 0 {
		return 0
	}
	return sum / float64(eligible)
}

func gini(values []Money) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]Money(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	var total, weighted int64
	for i, value := range copyValues {
		v := int64(value)
		if v < 0 {
			v = 0
		}
		total += v
		weighted += int64(i+1) * v
	}
	if total == 0 {
		return 0
	}
	n := int64(len(copyValues))
	return float64(2*weighted)/float64(n*total) - float64(n+1)/float64(n)
}

func polarization(opinions []int) float64 {
	if len(opinions) == 0 {
		return 0
	}
	values := append([]int(nil), opinions...)
	sort.Ints(values)
	median := values[len(values)/2]
	var distance int64
	for _, value := range values {
		d := value - median
		if d < 0 {
			d = -d
		}
		distance += int64(d)
	}
	return float64(distance) / float64(len(values)*BasisPoints)
}
