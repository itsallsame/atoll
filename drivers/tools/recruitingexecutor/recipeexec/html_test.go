package recipeexec

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func htmlListingSpec() recipeabi.Spec {
	return recipeabi.Spec{
		ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing, RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPHTML,
		Request: recipeabi.ReadRequest{Method: "GET", TimeoutMS: 2_000, MaxResponseBytes: 1 << 20, MaxRedirects: 1, UserAgent: "Atoll-Recruiting/1"},
		Extraction: recipeabi.Extraction{
			Collection: ".job", Next: "a.next", NextAttribute: "href",
			Fields:     map[string]string{"job_key": "a.role", "title": ".title", "activity_at": "time", "pinned": ".pin"},
			Attributes: map[string]string{"job_key": "data-job-id", "activity_at": "datetime", "pinned": "data-pinned"},
		},
		Listing: &recipeabi.ListingContract{IdentityField: "job_key", ActivityField: "activity_at", BoundaryMode: "activity_time",
			Ordering: "newest_activity_desc", UpdateRetop: true, OverlapPages: 2, MaxPages: 100, MaxItemsPerPage: 100, FrontierWidth: 20, ExcludePinnedField: "pinned"},
	}
}

func TestExecuteHTMLUsesScopedSelectorsAttributesAndText(t *testing.T) {
	document := []byte(`<!doctype html><html><body>
		  <article class="job"><i class="pin" data-pinned="true"></i></article>
      <article class="job"><a class="role" data-job-id="job-2"><span class="title"> Backend   Engineer </span></a><time datetime="2026-09-08T10:00:00Z"></time><i class="pin" data-pinned="false"></i></article>
      <article class="job"><a class="role" data-job-id="job-1"><span class="title">Frontend Engineer</span></a><time datetime="2026-09-08T09:00:00Z"></time><i class="pin" data-pinned="false"></i></article>
      <a class="next" href="/jobs?page=2">Next</a><script>window.sideEffect = true</script>
    </body></html>`)
	first, err := ExecuteHTML(htmlListingSpec(), document)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExecuteHTML(htmlListingSpec(), document)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("HTML offline replay is not deterministic: %+v %+v %v", first, second, err)
	}
	if len(first.Items) != 2 || !first.Quality.IdentityComplete || !first.Quality.OrderingContractHeld {
		t.Fatalf("unexpected HTML listing result: %+v", first)
	}
	var title, next string
	_ = json.Unmarshal(first.Items[0]["title"], &title)
	_ = json.Unmarshal(first.Next, &next)
	if title != "Backend Engineer" || next != "/jobs?page=2" {
		t.Fatalf("normalized title=%q next=%q", title, next)
	}
}

func TestExecuteHTMLRejectsInvalidSelectorAndMissingAttribute(t *testing.T) {
	spec := htmlListingSpec()
	spec.Extraction.Fields["title"] = "["
	if _, err := ExecuteHTML(spec, []byte(`<article class="job"></article>`)); err == nil {
		t.Fatal("invalid CSS selector was accepted")
	}
	spec = htmlListingSpec()
	if _, err := ExecuteHTML(spec, []byte(`<article class="job"><a class="role"></a><span class="title">x</span><time datetime="2026-09-08T10:00:00Z"></time><i class="pin" data-pinned="false"></i></article>`)); err == nil {
		t.Fatal("missing stable job identity attribute was accepted")
	}
}
