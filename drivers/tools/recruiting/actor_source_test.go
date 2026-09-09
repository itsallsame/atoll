package recruiting

import (
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestSourceEndpointUpdateIsPatchLikeAndRejectsNoop(t *testing.T) {
	source, err := model.NewRecruitmentSource("source-1", "company-1", "https://jobs.example.com/roles", "engineering", 1)
	if err != nil {
		t.Fatal(err)
	}
	category := "sales"
	next, err := applySourceEndpointUpdate(source, source.Version, sourceUpdatePayload{Category: &category})
	if err != nil {
		t.Fatal(err)
	}
	if next.CandidateEndpoint.URL != source.CandidateEndpoint.URL || next.CandidateEndpoint.Category != category || next.CandidateEndpoint.Revision != 2 {
		t.Fatalf("category patch = %+v", next.CandidateEndpoint)
	}
	endpoint := "HTTPS://JOBS.EXAMPLE.COM:443/roles"
	if _, err := applySourceEndpointUpdate(source, source.Version, sourceUpdatePayload{Endpoint: &endpoint}); err == nil {
		t.Fatal("canonical endpoint no-op was accepted")
	}
	if _, err := applySourceEndpointUpdate(source, source.Version, sourceUpdatePayload{}); err == nil {
		t.Fatal("empty source patch was accepted")
	}
	if _, err := applySourceEndpointUpdate(source, source.Version+1, sourceUpdatePayload{Endpoint: &endpoint}); err == nil {
		t.Fatal("stale no-op patch did not preserve version fencing")
	}
}

func TestSourceEventVocabularyAndManifest(t *testing.T) {
	words := map[string]string{
		TypeSourceAdd: "source.added", TypeSourceUpdate: "source.updated",
		TypeSourceValidate: "source.validation_started", TypeSourceValidationPublish: "source.validation.published",
		TypeSourceValidationReject: "source.validation.rejected",
		TypeSourcePause:            "source.paused",
		TypeSourceResume:           "source.resumed", TypeSourceArchive: "source.archived", TypeSourceRestore: "source.restored",
	}
	actorManifest := manifest()
	for word, event := range words {
		if actual := sourceEventKind(word); actual != event {
			t.Fatalf("event for %s = %s", word, actual)
		}
		if _, exists := actorManifest.Words[word]; !exists {
			t.Fatalf("source control word %s is absent from manifest", word)
		}
	}
	if _, exists := actorManifest.Words[TypeSourceList]; !exists {
		t.Fatal("source list is absent from manifest")
	}
	for _, word := range []string{TypeSourceDiscover, TypeSourceDiscoveryGet, TypeSourceDiscoveryCandidates,
		TypeSourceDiscoveryCandidateAccept, TypeSourceDiscoveryCandidateReject} {
		if _, exists := actorManifest.Words[word]; !exists {
			t.Fatalf("source discovery word %s is absent from manifest", word)
		}
	}
}
