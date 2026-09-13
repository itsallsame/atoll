package recruiting

import (
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestOnboardingResultRequiresEveryURLClassification(t *testing.T) {
	classified := []model.DiscoveryEvidenceNode{{Kind: model.EvidenceListURL, State: model.EvidenceValidated,
		RecruitmentType: model.RecruitmentURLCampus}}
	if !allURLsClassified(classified) {
		t.Fatal("classified campus URL was rejected")
	}
	classified[0].RecruitmentType = model.RecruitmentURLSpecial
	if allURLsClassified(classified) {
		t.Fatal("special URL without programme name was accepted")
	}
	classified[0].SpecialProgram = "Graduate Leadership Programme"
	if !allURLsClassified(classified) {
		t.Fatal("named special programme was rejected")
	}
	classified[0].RecruitmentType = model.RecruitmentURLUnknown
	if allURLsClassified(classified) {
		t.Fatal("unknown URL type was accepted")
	}
	if allURLsClassified(nil) {
		t.Fatal("empty URL result was classified as complete")
	}
}
