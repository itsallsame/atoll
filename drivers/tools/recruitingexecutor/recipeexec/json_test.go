package recipeexec

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func listingSpec() recipeabi.Spec {
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing, RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", TimeoutMS: 2_000, MaxResponseBytes: 1 << 20, MaxRedirects: 1, UserAgent: "Atoll-Recruiting/1"},
		Extraction: recipeabi.Extraction{Collection: "/data/jobs", Next: "/data/next", Fields: map[string]string{
			"job_key": "/id", "title": "/title", "activity_at": "/updated_at", "pinned": "/pinned",
		}},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", ActivityField: "activity_at", BoundaryMode: "activity_time",
			Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 2, MaxPages: 100, MaxItemsPerPage: 500, MaxTotalBytes: 10 << 20, FrontierWidth: 20, ExcludePinnedField: "pinned"},
	}
}

func TestExecuteJSONExtractsCanonicalRowsAndExcludesPinned(t *testing.T) {
	document := []byte(`{"data":{"jobs":[
		  {"pinned":true},
      {"title":"Backend","id":"job-2","pinned":false,"updated_at":"2026-09-08T10:00:00Z"},
      {"id":"job-1","title":"Frontend","updated_at":"2026-09-08T09:00:00Z","pinned":false}
    ],"next":"cursor-2"}}`)
	first, err := ExecuteJSON(listingSpec(), document)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExecuteJSON(listingSpec(), document)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("offline replay is not deterministic: %+v %+v %v", first, second, err)
	}
	if len(first.Items) != 2 || first.Quality.ItemCount != 2 || !first.Quality.IdentityComplete || !first.Quality.OrderingContractHeld {
		t.Fatalf("unexpected listing result: %+v", first)
	}
	var key string
	if err := json.Unmarshal(first.Items[0]["job_key"], &key); err != nil || key != "job-2" {
		t.Fatalf("first normalized job key=%q err=%v", key, err)
	}
	var next string
	if err := json.Unmarshal(first.Next, &next); err != nil || next != "cursor-2" {
		t.Fatalf("next=%q err=%v", next, err)
	}
}

func TestExecuteJSONReportsOrderingAndIdentityContractViolations(t *testing.T) {
	document := []byte(`{"data":{"jobs":[
      {"id":"job-1","title":"Old","updated_at":"2026-09-08T09:00:00Z","pinned":false},
      {"id":"","title":"New","updated_at":"2026-09-08T10:00:00Z","pinned":false}
    ],"next":null}}`)
	result, err := ExecuteJSON(listingSpec(), document)
	if err != nil {
		t.Fatal(err)
	}
	if result.Quality.IdentityComplete || result.Quality.OrderingContractHeld || result.Quality.MayAdvanceCheckpoint() {
		t.Fatalf("contract violation was not preserved in quality proof: %+v", result.Quality)
	}
}

func TestExecuteJSONAcceptsIntegerIdentityWithoutLosingPrecision(t *testing.T) {
	document := []byte(`{"data":{"jobs":[
      {"id":80720940000000000001,"title":"Backend","updated_at":"2026-09-08T10:00:00Z","pinned":false}
    ],"next":null}}`)
	result, err := ExecuteJSON(listingSpec(), document)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Quality.IdentityComplete {
		t.Fatalf("integer identity was rejected: %+v", result.Quality)
	}
	scan, err := NewListingScan(listingSpec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := scan.AddPage(result); err != nil {
		t.Fatal(err)
	}
	candidate, err := scan.CheckpointCandidate()
	if err != nil || len(candidate.FrontierKeys) != 1 || candidate.FrontierKeys[0] != "80720940000000000001" {
		t.Fatalf("numeric identity was not canonical and lossless: candidate=%+v err=%v", candidate, err)
	}
}

func TestRawIdentityRejectsAmbiguousJSONScalars(t *testing.T) {
	for _, raw := range []string{"1.0", "1e3", "true", "null", `{}`, `[]`, `""`} {
		identity, ok := rawIdentity(json.RawMessage(raw))
		if ok && identity != "" {
			t.Fatalf("ambiguous identity %s was accepted as %q", raw, identity)
		}
	}
}

func TestExecuteJSONRejectsMissingFieldsAndTrailingInput(t *testing.T) {
	for name, document := range map[string][]byte{
		"missing field": []byte(`{"data":{"jobs":[{"id":"job-1"}],"next":null}}`),
		"second value":  []byte(`{"data":{"jobs":[],"next":null}} {}`),
		"trailing junk": []byte(`{"data":{"jobs":[],"next":null}} !`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ExecuteJSON(listingSpec(), document); err == nil {
				t.Fatal("invalid saved response was accepted")
			}
		})
	}
}

func TestJSONPointerSupportsRFC6901Escapes(t *testing.T) {
	root := map[string]any{"a/b": map[string]any{"~key": json.Number("3")}}
	value, err := resolvePointer(root, "/a~1b/~0key")
	if err != nil || value != json.Number("3") {
		t.Fatalf("escaped pointer value=%v err=%v", value, err)
	}
	if _, err := resolvePointer(root, "/bad~2escape"); err == nil {
		t.Fatal("invalid JSON pointer escape was accepted")
	}
}
