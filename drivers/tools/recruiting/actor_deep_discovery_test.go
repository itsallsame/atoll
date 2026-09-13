package recruiting

import (
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDeepDiscoveryCheckpointResolvesReadableGraphRefs(t *testing.T) {
	nodes, edges, err := normalizeDeepDiscoveryGraph([]deepDiscoveryNodeInput{
		{Ref: "company", Kind: model.EvidenceCompany, CanonicalValue: "Example", Label: "Example", State: model.EvidenceValidated, Sensor: model.SensorHuman, EvidenceURL: "https://example.com", Basis: "operator scope"},
		{Ref: "brand", Kind: model.EvidenceBrand, CanonicalValue: "Example Cloud", Label: "Example Cloud", State: model.EvidenceValidated, Sensor: model.SensorOfficialSite, EvidenceURL: "https://example.com/cloud", Basis: "official brand navigation"},
	}, []deepDiscoveryEdgeInput{{From: "company", To: "brand", Relation: "owns_brand", Basis: "official corporate page"}})
	if err != nil || len(nodes) != 2 || len(edges) != 1 {
		t.Fatalf("graph=%+v/%+v err=%v", nodes, edges, err)
	}
	if edges[0].FromNodeID != nodes[0].NodeID || edges[0].ToNodeID != nodes[1].NodeID {
		t.Fatalf("refs were not resolved: %+v", edges[0])
	}
	if _, _, err := normalizeDeepDiscoveryGraph([]deepDiscoveryNodeInput{{Ref: "same"}, {Ref: "same"}}, nil); err == nil {
		t.Fatal("duplicate local refs accepted")
	}
}
