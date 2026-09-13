package recruitingexecutor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLookupOfficialWebsitesReturnsBoundedAuditableCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/w/api.php":
			if request.URL.Query().Get("search") != "字节跳动" || request.URL.Query().Get("limit") != "5" {
				t.Fatalf("unexpected search query: %s", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{"search":[{"id":"Q1","label":"ByteDance","description":"technology company"}]}`))
		case "/wiki/Special:EntityData/Q1.json":
			_, _ = response.Write([]byte(`{"entities":{"Q1":{"claims":{"P856":[{"mainsnak":{"datavalue":{"value":"https://www.bytedance.com/"}}}]}}}}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	candidates, err := lookupOfficialWebsites(context.Background(), server.Client(), server.URL, "字节跳动", "zh", nil)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	candidate := candidates[0]
	if candidate.EntityID != "Q1" || candidate.Website != "https://www.bytedance.com" || candidate.ClaimedWebsite != candidate.Website ||
		candidate.EvidenceURL != server.URL+"/wiki/Special:EntityData/Q1.json" || candidate.ConfidenceBasis == "" {
		t.Fatalf("candidate is not normalized and auditable: %+v", candidate)
	}
}

func TestWebsiteLookupRejectsUnsafeFacts(t *testing.T) {
	for _, value := range []string{"", "file:///etc/passwd", "https://localhost/jobs", "http://127.0.0.1/jobs", "https://x.example/jobs#fragment"} {
		if safeWebsiteCandidate(value) {
			t.Fatalf("unsafe website candidate accepted: %q", value)
		}
	}
	for _, value := range []string{"Q", "Qabc", "P123", "../Q1"} {
		if safeEntityID(value) {
			t.Fatalf("unsafe entity ID accepted: %q", value)
		}
	}
}
