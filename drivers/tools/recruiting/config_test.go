package recruiting

import (
	"encoding/json"
	"testing"
)

func TestParseConfigDefaultsAndRejectsUnknownFields(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil || cfg.ExecutorID != "recruiting-executor" || cfg.DatabaseDSNEnv != "ATOLL_RECRUITING_MYSQL_DSN" {
		t.Fatalf("default config = %+v, %v", cfg, err)
	}
	if _, err := parseConfig(json.RawMessage(`{"unknown":true}`)); err == nil {
		t.Fatal("unknown config field was accepted")
	}
	if _, err := parseConfig(json.RawMessage(`{"executor_id":" "}`)); err == nil {
		t.Fatal("blank executor identity was accepted")
	}
	if _, err := parseConfig(json.RawMessage(`{"database_dsn_env":" "}`)); err == nil {
		t.Fatal("blank database DSN environment name was accepted")
	}
}

func TestManifestExposesControlAndExecutorResultWords(t *testing.T) {
	words := manifest().Words
	for _, word := range []string{
		TypeProbeStart, TypeProbeSchedule, TypeProbeStatus, TypeExecutionResult,
		TypeCompanyAdd, TypeCompanyUpdate, TypeCompanyPause, TypeCompanyResume,
		TypeCompanyArchive, TypeCompanyRestore, TypeCompanyGet, TypeCompanyList,
	} {
		if _, ok := words[word]; !ok {
			t.Fatalf("manifest does not expose %q", word)
		}
	}
}
