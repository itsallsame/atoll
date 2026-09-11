package recruiting

import (
	"encoding/json"
	"testing"

	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
)

func TestCompanyCommandHashIsStableAcrossAuthenticatedSessions(t *testing.T) {
	envelope := message.Envelope{
		Type: TypeCompanyPause, Payload: json.RawMessage(`{"command_id":"same"}`),
		Sender: message.Sender{Kind: actor.KindHuman, ID: "human:alice"},
	}
	first := commandRequestHash(actorbase.Msg{Envelope: envelope})
	envelope.Sender.ID = "human:bob"
	second := commandRequestHash(actorbase.Msg{Envelope: envelope})
	if first != second {
		t.Fatal("command receipt could not replay after authenticated session rotation")
	}
}

func TestCompanyEventVocabularyIsPastTense(t *testing.T) {
	for word, expected := range map[string]string{
		TypeCompanyAdd: "company.added", TypeCompanyUpdate: "company.updated",
		TypeCompanyWebsiteRollback: "company.website_rolled_back",
		TypeCompanyPause:           "company.paused", TypeCompanyResume: "company.resumed",
		TypeCompanyArchive: "company.archived", TypeCompanyRestore: "company.restored",
	} {
		if actual := companyEventKind(word); actual != expected {
			t.Fatalf("event for %s = %s", word, actual)
		}
	}
}
