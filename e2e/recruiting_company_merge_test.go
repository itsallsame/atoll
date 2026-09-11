package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingOperatorLogicallyMergesAndReversesCompanies(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("company-merge-operator", "company-merge-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})

	const controlName = "company-merge-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting", "description": "Logical company merge control.",
		"config": map[string]any{"executor_id": "unused-company-merge-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false}, "visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	addCompany := func(id, website string) map[string]any {
		return ws.request(homeID, "recruiting.company.add", controlID, map[string]any{
			"command_id": "e2e-company-add-" + id, "company_id": id, "name": "Company " + id,
			"website": website, "reason": "prepare merge fixture",
		})
	}
	canonical := addCompany("e2e-merge-canonical", "https://canonical.e2e-merge.example")
	aliasA := addCompany("e2e-merge-alias-a", "https://a.e2e-merge.example")
	aliasB := addCompany("e2e-merge-alias-b", "https://b.e2e-merge.example")
	ws.request(homeID, "recruiting.source.add", controlID, map[string]any{
		"command_id": "e2e-merge-alias-source-add", "source_id": "e2e-merge-alias-source",
		"company_id": "e2e-merge-alias-a", "endpoint": "https://jobs.e2e-merge.example/a",
		"category": "all", "discovery_generation": 1, "reason": "prove merge retains source ownership",
	})
	versions := map[string]any{
		"e2e-merge-canonical": nestedNumberField(t, canonical, "company", "version"),
		"e2e-merge-alias-a":   nestedNumberField(t, aliasA, "company", "version"),
		"e2e-merge-alias-b":   nestedNumberField(t, aliasB, "company", "version"),
	}
	previewPayload := map[string]any{
		"command_id": "e2e-company-merge-preview", "merge_preview_id": "e2e-company-merge-1", "action": "merge",
		"canonical_company_id": "e2e-merge-canonical", "alias_company_ids": []string{"e2e-merge-alias-b", "e2e-merge-alias-a"},
		"expected_versions": versions, "reason": "operator verified duplicate legal entities",
	}
	preview := ws.request(homeID, "recruiting.company.merge.preview", controlID, previewPayload)
	if nestedStringField(t, preview, "preview", "status") != "ready" ||
		nestedStringField(t, preview, "preview", "preview_hash") == "" ||
		nestedNumberField(t, preview, "preview", "version") != 1 ||
		!strings.HasPrefix(stringField(t, preview, "requested_by"), "human:company-merge-operator:") {
		t.Fatalf("company merge preview=%v", preview)
	}
	members := nestedSliceField(t, preview, "preview", "members")
	if len(members) != 2 || stringField(t, members[0].(map[string]any), "company_id") != "e2e-merge-alias-a" ||
		numberField(t, members[0].(map[string]any), "source_count") != 1 {
		t.Fatalf("company merge impact=%v", members)
	}
	confirmPayload := map[string]any{
		"command_id": "e2e-company-merge-confirm", "merge_preview_id": "e2e-company-merge-1",
		"expected_version": 1, "preview_hash": nestedStringField(t, preview, "preview", "preview_hash"),
		"reason": "confirm the reviewed logical mapping",
	}
	confirmed := ws.request(homeID, "recruiting.company.merge.confirm", controlID, confirmPayload)
	if nestedStringField(t, confirmed, "preview", "status") != "confirmed" ||
		nestedNumberField(t, confirmed, "preview", "version") != 2 || len(sliceField(t, confirmed, "aliases")) != 2 {
		t.Fatalf("company merge confirmation=%v", confirmed)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("company-merge-operator@example.test", "operator-local-password"); login["id"] != "company-merge-operator" {
		t.Fatalf("company merge operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	replayed := recovered.request(homeID, "recruiting.company.merge.confirm", controlID, confirmPayload)
	if nestedStringField(t, replayed, "preview", "status") != "confirmed" || len(sliceField(t, replayed, "aliases")) != 2 {
		t.Fatalf("company merge replay after restart=%v", replayed)
	}
	source := recovered.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": "e2e-merge-alias-source"})
	if nestedStringField(t, source, "entity", "company_id") != "e2e-merge-alias-a" {
		t.Fatalf("logical merge rewrote Source ownership=%v", source)
	}
	aliasView := recovered.request(homeID, "recruiting.company.get", controlID, map[string]any{"company_id": "e2e-merge-alias-a"})
	if aliasView["is_alias"] != true || stringField(t, aliasView, "canonical_company_id") != "e2e-merge-canonical" {
		t.Fatalf("company get omitted canonical mapping=%v", aliasView)
	}
	if _, terminal, err := recovered.tryRequest(homeID, "recruiting.source.add", controlID, map[string]any{
		"command_id": "e2e-merge-blocked-source-add", "source_id": "e2e-merge-blocked-source",
		"company_id": "e2e-merge-alias-a", "endpoint": "https://jobs.e2e-merge.example/blocked",
		"category": "all", "discovery_generation": 1, "reason": "must use canonical company",
	}); err == nil || terminal["error_code"] != "business_key_conflict" {
		t.Fatalf("active alias accepted a new Source terminal=%v err=%v", terminal, err)
	}

	reversePreviewPayload := map[string]any{
		"command_id": "e2e-company-merge-reverse-preview", "merge_preview_id": "e2e-company-merge-reverse-1", "action": "reverse",
		"canonical_company_id": "e2e-merge-canonical", "alias_company_ids": []string{"e2e-merge-alias-a"},
		"expected_versions": map[string]any{"e2e-merge-canonical": 1, "e2e-merge-alias-a": 1},
		"reason":            "operator found the alias is a distinct legal entity",
	}
	reversePreview := recovered.request(homeID, "recruiting.company.merge.preview", controlID, reversePreviewPayload)
	reversed := recovered.request(homeID, "recruiting.company.merge.confirm", controlID, map[string]any{
		"command_id": "e2e-company-merge-reverse-confirm", "merge_preview_id": "e2e-company-merge-reverse-1",
		"expected_version": 1, "preview_hash": nestedStringField(t, reversePreview, "preview", "preview_hash"),
		"reason": "confirm the reviewed reversal",
	})
	if nestedStringField(t, reversed, "preview", "action") != "reverse" ||
		nestedStringField(t, reversed, "preview", "status") != "confirmed" {
		t.Fatalf("company merge reversal=%v", reversed)
	}
	restoredSource := recovered.request(homeID, "recruiting.source.add", controlID, map[string]any{
		"command_id": "e2e-merge-restored-source-add", "source_id": "e2e-merge-restored-source",
		"company_id": "e2e-merge-alias-a", "endpoint": "https://jobs.e2e-merge.example/restored",
		"category": "all", "discovery_generation": 1, "reason": "alias is independent after reversal",
	})
	if nestedStringField(t, restoredSource, "source", "company_id") != "e2e-merge-alias-a" {
		t.Fatalf("reversed company could not own a Source=%v", restoredSource)
	}

	db, err := store.Open(runtimeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var active, historical, sourceOwner, events, blockedReceipts int
	if err := db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM recruiting_company_aliases WHERE alias_company_id = 'e2e-merge-alias-a' AND active_alias_key IS NOT NULL),
  (SELECT COUNT(*) FROM recruiting_company_aliases WHERE alias_company_id = 'e2e-merge-alias-a' AND effective_until IS NOT NULL AND ended_by_merge_preview_id = 'e2e-company-merge-reverse-1'),
  (SELECT COUNT(*) FROM recruiting_sources WHERE source_id = 'e2e-merge-alias-source' AND company_id = 'e2e-merge-alias-a'),
	(SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_kind IN ('company.merge.previewed','company.merge.confirmed','company.merge.reversed')),
  (SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = 'e2e-merge-blocked-source-add')`).
		Scan(&active, &historical, &sourceOwner, &events, &blockedReceipts); err != nil {
		t.Fatal(err)
	}
	if active != 0 || historical != 1 || sourceOwner != 1 || events != 4 || blockedReceipts != 0 {
		t.Fatalf("company merge persisted facts active=%d historical=%d source_owner=%d events=%d blocked_receipts=%d",
			active, historical, sourceOwner, events, blockedReceipts)
	}
}

func nestedSliceField(t *testing.T, value map[string]any, object, field string) []any {
	t.Helper()
	nested, _ := value[object].(map[string]any)
	items, ok := nested[field].([]any)
	if !ok {
		t.Fatalf("%s.%s is not a list: %v", object, field, value)
	}
	return items
}

func sliceField(t *testing.T, value map[string]any, field string) []any {
	t.Helper()
	items, ok := value[field].([]any)
	if !ok {
		t.Fatalf("%s is not a list: %v", field, value)
	}
	return items
}
