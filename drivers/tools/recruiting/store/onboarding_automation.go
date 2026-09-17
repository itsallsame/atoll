package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// DeepDiscoveryBrowserAutomationFact is a bounded control-plane projection
// used by natural-language onboarding. It does not create a second scheduler;
// mutations still go through the normal Mission/Work command transactions.
type DeepDiscoveryBrowserAutomationFact struct {
	Probe  model.DeepDiscoveryBrowserProbe
	Work   model.Work
	Result *DeepDiscoveryBrowserResult
}

type DeepDiscoveryAutomationSnapshot struct {
	BrowserProbes []DeepDiscoveryBrowserAutomationFact
}

type SourceInitializationMission struct {
	Mission model.DeepDiscoveryMission
	Company model.Company
}

// ListActiveSourceInitializationMissions is the durable recovery view for the
// onboarding state machine. It deliberately excludes ordinary research and
// repair Missions: only the bounded source-initialization workflow is safe to
// advance without a human or Steward decision.
func (r *Repository) ListActiveSourceInitializationMissions(ctx context.Context, limit int) ([]SourceInitializationMission, error) {
	if limit < 1 || limit > 500 {
		return nil, fmt.Errorf("limit must be in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT m.state_json,c.state_json
FROM recruiting_deep_discovery_missions m
JOIN recruiting_companies c ON c.company_id=m.company_id
WHERE m.mission_status='active'
  AND (JSON_UNQUOTE(JSON_EXTRACT(m.state_json,'$.purpose'))=?
       OR (JSON_EXTRACT(m.state_json,'$.purpose') IS NULL
           AND m.mission_id LIKE 'mission-auto-initialization-%'))
ORDER BY m.updated_at,m.mission_id LIMIT ?`, model.DeepDiscoveryPurposeSourceInitialization, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]SourceInitializationMission, 0)
	for rows.Next() {
		var missionRaw, companyRaw []byte
		if err := rows.Scan(&missionRaw, &companyRaw); err != nil {
			return nil, err
		}
		var item SourceInitializationMission
		if err := json.Unmarshal(missionRaw, &item.Mission); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(companyRaw, &item.Company); err != nil {
			return nil, err
		}
		if item.Mission.Status == model.DeepDiscoveryActive && item.Mission.IsSourceInitializationEvidence() {
			result = append(result, item)
		}
	}
	return result, rows.Err()
}

func (r *Repository) GetDeepDiscoveryAutomationSnapshot(ctx context.Context,
	missionID string) (DeepDiscoveryAutomationSnapshot, error) {
	missionID = strings.TrimSpace(missionID)
	if missionID == "" {
		return DeepDiscoveryAutomationSnapshot{}, fmt.Errorf("Mission ID is required")
	}
	snapshot := DeepDiscoveryAutomationSnapshot{}
	rows, err := r.db.QueryContext(ctx, `SELECT p.state_json,w.state_json,p.result_json
FROM recruiting_deep_discovery_browser_probes p
JOIN recruiting_works w ON w.work_id=p.work_id
WHERE p.mission_id=? ORDER BY p.created_at,p.probe_id LIMIT 2001`, missionID)
	if err != nil {
		return DeepDiscoveryAutomationSnapshot{}, err
	}
	for rows.Next() {
		if len(snapshot.BrowserProbes) == 2000 {
			_ = rows.Close()
			return DeepDiscoveryAutomationSnapshot{}, fmt.Errorf("Deep Discovery browser history exceeds Mission operation bound")
		}
		var probeRaw, workRaw []byte
		var resultRaw sql.RawBytes
		if err := rows.Scan(&probeRaw, &workRaw, &resultRaw); err != nil {
			_ = rows.Close()
			return DeepDiscoveryAutomationSnapshot{}, err
		}
		fact := DeepDiscoveryBrowserAutomationFact{}
		if err := json.Unmarshal(probeRaw, &fact.Probe); err != nil {
			_ = rows.Close()
			return DeepDiscoveryAutomationSnapshot{}, err
		}
		if err := json.Unmarshal(workRaw, &fact.Work); err != nil {
			_ = rows.Close()
			return DeepDiscoveryAutomationSnapshot{}, err
		}
		if len(resultRaw) != 0 {
			var result DeepDiscoveryBrowserResult
			if err := json.Unmarshal(resultRaw, &result); err != nil {
				_ = rows.Close()
				return DeepDiscoveryAutomationSnapshot{}, err
			}
			if err := canonicalizeBrowserResultEvidence(&result); err != nil {
				_ = rows.Close()
				return DeepDiscoveryAutomationSnapshot{}, err
			}
			fact.Result = &result
		}
		snapshot.BrowserProbes = append(snapshot.BrowserProbes, fact)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return DeepDiscoveryAutomationSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return DeepDiscoveryAutomationSnapshot{}, err
	}

	return snapshot, nil
}
