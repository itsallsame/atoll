package main

import (
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
	handler, err := siteHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, want string
	}{
		{"/", "名册之外"},
		{"/runs/day-01/ledger.jsonl", "E001"},
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
	if _, err := siteHandler(t.TempDir()); err == nil {
		t.Fatal("expected missing index error")
	}
}
