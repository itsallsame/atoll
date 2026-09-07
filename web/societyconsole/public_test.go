package societyconsole

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestPublicSocietyNeedsNoLoginAndSharesOneTally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "public-society.json")
	service, err := NewPublicService(path)
	if err != nil {
		t.Fatal(err)
	}

	firstIssue, firstCookie := publicRequest(t, service, nil, "society.experiment.issue", map[string]any{})
	if firstIssue["round_id"] != float64(1) || firstIssue["voter_count"] != float64(0) || firstCookie == nil {
		t.Fatalf("first issue=%v cookie=%v", firstIssue, firstCookie)
	}
	secondIssue, secondCookie := publicRequest(t, service, nil, "society.experiment.issue", map[string]any{})
	if secondCookie == nil || secondCookie.Value == firstCookie.Value || secondIssue["round_id"] != float64(1) {
		t.Fatalf("second issue=%v cookie=%v", secondIssue, secondCookie)
	}

	voted, _ := publicRequest(t, service, firstCookie, "society.experiment.vote", map[string]any{"command_id": "vote-1", "public_ai": 6, "transition_fund": 2, "ai_dividend": 2})
	if voted["voter_count"] != float64(1) || voted["my_ballot"] == nil {
		t.Fatalf("voted=%v", voted)
	}
	shared, _ := publicRequest(t, service, secondCookie, "society.experiment.issue", map[string]any{})
	if shared["voter_count"] != float64(1) || shared["my_ballot"] != nil {
		t.Fatalf("shared issue=%v", shared)
	}

	restarted, err := NewPublicService(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := publicRequest(t, restarted, secondCookie, "society.experiment.issue", map[string]any{})
	if persisted["voter_count"] != float64(1) {
		t.Fatalf("persisted issue=%v", persisted)
	}
}

func publicRequest(t *testing.T, service http.Handler, cookie *http.Cookie, word string, payload any) (map[string]any, *http.Cookie) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"type": word, "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/society", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var issued *http.Cookie
	if cookies := rec.Result().Cookies(); len(cookies) > 0 {
		issued = cookies[0]
	}
	return result, issued
}
