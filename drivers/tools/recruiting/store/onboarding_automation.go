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

type DeepDiscoveryPublicQueryAutomationFact struct {
	Verification model.DeepDiscoveryPublicQueryVerification
	Work         model.Work
}

type DeepDiscoveryAutomationSnapshot struct {
	BrowserProbes      []DeepDiscoveryBrowserAutomationFact
	QueryVerifications []DeepDiscoveryPublicQueryAutomationFact
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

	rows, err = r.db.QueryContext(ctx, `SELECT q.state_json,w.state_json
FROM recruiting_deep_discovery_public_query_verifications q
JOIN recruiting_works w ON w.work_id=q.work_id
WHERE q.mission_id=? ORDER BY q.created_at,q.verification_id LIMIT 2001`, missionID)
	if err != nil {
		return DeepDiscoveryAutomationSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(snapshot.QueryVerifications) == 2000 {
			return DeepDiscoveryAutomationSnapshot{}, fmt.Errorf("public-query verification history exceeds Mission operation bound")
		}
		var verificationRaw, workRaw []byte
		if err := rows.Scan(&verificationRaw, &workRaw); err != nil {
			return DeepDiscoveryAutomationSnapshot{}, err
		}
		fact := DeepDiscoveryPublicQueryAutomationFact{}
		if err := json.Unmarshal(verificationRaw, &fact.Verification); err != nil {
			return DeepDiscoveryAutomationSnapshot{}, err
		}
		if err := json.Unmarshal(workRaw, &fact.Work); err != nil {
			return DeepDiscoveryAutomationSnapshot{}, err
		}
		snapshot.QueryVerifications = append(snapshot.QueryVerifications, fact)
	}
	return snapshot, rows.Err()
}
