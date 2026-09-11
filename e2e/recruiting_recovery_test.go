package e2e

import (
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	_ "modernc.org/sqlite"
)

// TestRecruitingRecoveryAcrossServerRestart closes the application/Atoll
// crash cut that separate Repository and runtime tests cannot prove together:
// a domain event already exists in the Atoll ledger, its SQL delivery
// checkpoint is lost, the server is killed, and the same stable event is
// emitted again after restart. The Atoll ledger must retain exactly one row.
// The same restart also proves a user's closed-report recovery command keeps
// its unique causal head and does not rewrite the historical DailyRun.
func TestRecruitingRecoveryAcrossServerRestart(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()

	operator := newAPIClient(t, h.base)
	registered := operator.register("recovery-operator", "recovery-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlName = "recovery-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting", "description": "Recruiting crash recovery control.",
		"config": map[string]any{"executor_id": "unused-recovery-executor", "reconcile_interval_ms": 30000,
			"daily_schedule_enabled": false}, "visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)

	companyCommand := map[string]any{"command_id": "e2e-recovery-company-add", "company_id": "e2e-recovery-company",
		"name": "Recovery Company", "website": "https://recovery.example.test", "reason": "create one durable outbox event"}
	createdCompany := ws.request(homeID, "recruiting.company.add", controlID, companyCommand)
	if nestedStringField(t, createdCompany, "company", "company_id") != "e2e-recovery-company" {
		t.Fatalf("recovery company=%v", createdCompany)
	}

	const sourceID = "e2e-recovery-source"
	now := time.Now().UTC().Truncate(time.Second)
	seedReadyRecruitingSource(t, runtimeDSN, sourceID, now.Add(-3*time.Hour))
	daily, occurrence := seedClosedUncoveredDailyRun(t, runtimeDSN, sourceID, now)
	source := ws.request(homeID, "recruiting.source.get", controlID, map[string]any{"id": sourceID})
	recoveryCommand := map[string]any{
		"command_id": "e2e-recovery-run-create", "run_id": "e2e-recovery-listing-run", "work_id": "e2e-recovery-work",
		"recovery_of_occurrence_id": occurrence.OccurrenceID,
		"target":                    map[string]any{"target_type": "source", "target_id": sourceID},
		"expected_version":          nestedNumberField(t, source, "entity", "version"),
		"reason":                    "repair one immutable closed-report gap",
	}
	createdRecovery := ws.request(homeID, "recruiting.run.production", controlID, recoveryCommand)
	if nestedStringField(t, createdRecovery, "listing_run", "recovery_of_occurrence_id") != occurrence.OccurrenceID {
		t.Fatalf("daily recovery lost lineage=%v", createdRecovery)
	}

	reconciled := ws.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 500})
	if numberField(t, reconciled, "delivered") < 1 {
		t.Fatalf("initial event reconcile=%v", reconciled)
	}
	mysqlDB, eventID := openRecoveryEvent(t, runtimeDSN, "e2e-recovery-company-add")
	defer mysqlDB.Close()
	assertRecruitingEventState(t, mysqlDB, eventID, "delivered")
	channelDB := openRecoveryChannelDB(t, h.serverHome, homeID)
	defer channelDB.Close()
	if countRecoveryLedgerEvent(t, channelDB, eventID) != 1 {
		t.Fatalf("initial Atoll ledger does not contain exactly one %s event", eventID)
	}

	// This SQL update is an explicit fault injection, not a product repair:
	// model the process dying after the Atoll append committed but before the
	// application delivery checkpoint became durable.
	result, err := mysqlDB.Exec(`UPDATE recruiting_event_outbox
SET delivery_status = 'pending', delivered_at = NULL, delivery_attempts = 0,
    last_error_class = NULL, next_attempt_at = UTC_TIMESTAMP(6)
WHERE event_id = ? AND delivery_status = 'delivered'`, eventID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		t.Fatalf("fault injection changed %d event rows", changed)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("recovery-operator@example.test", "operator-local-password"); login["id"] != "recovery-operator" {
		t.Fatalf("recovery operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	second := recovered.request(homeID, "recruiting.system.reconcile", controlID, map[string]any{"limit": 500})
	if numberField(t, second, "delivered") < 1 {
		t.Fatalf("restarted event reconcile=%v", second)
	}
	assertRecruitingEventState(t, mysqlDB, eventID, "delivered")
	if countRecoveryLedgerEvent(t, channelDB, eventID) != 1 {
		t.Fatalf("duplicate delivery created another Atoll ledger row for %s", eventID)
	}

	replayedCompany := recovered.request(homeID, "recruiting.company.add", controlID, companyCommand)
	if nestedNumberField(t, replayedCompany, "company", "version") != 1 {
		t.Fatalf("company command did not replay after restart=%v", replayedCompany)
	}
	replayedRecovery := recovered.request(homeID, "recruiting.run.production", controlID, recoveryCommand)
	if nestedStringField(t, replayedRecovery, "listing_run", "listing_run_id") != "e2e-recovery-listing-run" ||
		nestedStringField(t, replayedRecovery, "work", "work_id") != "e2e-recovery-work" {
		t.Fatalf("daily recovery created a competing head after restart=%v", replayedRecovery)
	}
	summary := recovered.request(homeID, "recruiting.daily_run.summary", controlID,
		map[string]any{"id": daily.DailyRunID, "limit": 10})
	assertDailyRecoveryProgress(t, summary, 1, 0)
	if nestedStringField(t, summary, "daily_run", "daily_run_status") != "completed_with_exceptions" ||
		nestedNumberField(t, summary, "daily_run", "version") != float64(daily.Version) {
		t.Fatalf("recovery rewrote immutable DailyRun=%v", summary)
	}
}

func openRecoveryEvent(t *testing.T, dsn, causeCommandID string) (*sql.DB, string) {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := db.QueryRow(`SELECT event_id FROM recruiting_event_outbox WHERE cause_command_id = ?`, causeCommandID).Scan(&eventID); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db, eventID
}

func openRecoveryChannelDB(t *testing.T, serverHome, channelID string) *sql.DB {
	t.Helper()
	encoded := base64.RawURLEncoding.EncodeToString([]byte(channelID))
	path := filepath.Join(serverHome, "channels", encoded+".db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func countRecoveryLedgerEvent(t *testing.T, db *sql.DB, eventID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE id = ? AND kind = 'event'`, eventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertRecruitingEventState(t *testing.T, db *sql.DB, eventID, status string) {
	t.Helper()
	var stored string
	if err := db.QueryRow(`SELECT delivery_status FROM recruiting_event_outbox WHERE event_id = ?`, eventID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != status {
		t.Fatalf("event %s status=%s want=%s", eventID, stored, status)
	}
}
