package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSiteHandlerServesPageAndRunData(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "runs", "day-01"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site", "index.html"), []byte("<h1>名册之外</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runs", "day-01", "ledger.jsonl"), []byte("{\"event_id\":\"E001\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"world.yaml":        "geography:\n  county_relation: 归鹭洲\n",
		"cast.yaml":         "characters:\n  - id: shen_yanqiu\n    name: 沈砚秋\n",
		"constitution.yaml": "work_title: 名册之外\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := siteHandler(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, want string
	}{
		{"/", "名册之外"},
		{"/api/world", "归鹭洲"},
		{"/api/cast", "沈砚秋"},
		{"/api/constitution", "名册之外"},
		{"/healthz", `"status":"ok"`},
	} {
		req := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) {
			t.Fatalf("GET %s: code=%d body=%q", test.path, response.Code, response.Body.String())
		}
	}
}

func TestSiteHandlerRequiresIndex(t *testing.T) {
	if _, err := siteHandler(t.TempDir(), ""); err == nil {
		t.Fatal("expected missing index error")
	}
}

func TestSiteDoesNotExposePrivateRunArtifacts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site", "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := siteHandler(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/runs/day-03/beliefs.json", "/runs/day-03/memories.json", "/runs/day-03/proposals.json", "/runs/day-03/ledger.jsonl"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("GET %s code=%d want 404", path, response.Code)
		}
	}
}

func TestPublicProjectionUsesAtollFactsAfterCalibrationBoundary(t *testing.T) {
	content := t.TempDir()
	for _, day := range []string{"day-01", "day-03"} {
		if err := os.MkdirAll(filepath.Join(content, "runs", day), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(content, "runs", day, "chapter.md"), []byte("# 章\n\n正文"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	index := `[{"day":1,"chapter_title":"校准","pov":"甲","directory":"day-01"},{"day":3,"chapter_title":"接管","pov":"乙","directory":"day-03"}]`
	if err := os.WriteFile(filepath.Join(content, "runs", "index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "runs", "day-01", "ledger.jsonl"), []byte("{\"event_id\":\"E001\",\"world_state_delta\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atollHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(atollHome, "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(atollHome, "channels", "YzA.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE messages (seq INTEGER PRIMARY KEY, type TEXT, kind TEXT, payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages(seq,type,kind,payload) VALUES(1,'narrative.fact','event',?)`, `{"day":3,"tick":"dawn","event_id":"D03-E001","world_state_delta":{}}`); err != nil {
		t.Fatal(err)
	}
	projection, err := buildPublicProjection(content, atollHome)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Authority != "atoll-c0" || projection.DurableDay != 3 || projection.FactCount != 1 || len(projection.Runs) != 2 {
		t.Fatalf("projection=%+v", projection)
	}
	if projection.Runs[0].Source != "author-calibration" || projection.Runs[1].Source != "atoll" {
		t.Fatalf("sources=%q,%q", projection.Runs[0].Source, projection.Runs[1].Source)
	}
}
