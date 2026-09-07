package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandReceiptReplaysStableResponseOnlyForSameRequest(t *testing.T) {
	receipt, err := NewCommandReceipt("cmd-1", "recruiting.company.pause", "sha256:request-a", json.RawMessage(`{"version":2,"status":"paused"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := receipt.Replay("sha256:request-a")
	if err != nil || string(response) != `{"version":2,"status":"paused"}` {
		t.Fatalf("stable replay = %s %v", response, err)
	}
	response[0] = '['
	again, err := receipt.Replay("sha256:request-a")
	if err != nil || string(again) != `{"version":2,"status":"paused"}` {
		t.Fatal("caller mutated stored receipt response")
	}
	if _, err := receipt.Replay("sha256:request-b"); err == nil {
		t.Fatal("command ID reuse with different input was accepted")
	}
}

func TestBusinessKeysAreStableAndIndependentFromCommandID(t *testing.T) {
	detail1, _ := DetailWorkKey("source-1", "external-1", 3, "detail_sync")
	detail2, _ := DetailWorkKey("source-1", "external-1", 3, "detail_sync")
	if detail1 != detail2 {
		t.Fatalf("detail key is unstable: %q %q", detail1, detail2)
	}
	baseline1, _ := BaselineWorkKey("source-1", 7)
	baseline2, _ := BaselineWorkKey("source-1", 7)
	if baseline1 != baseline2 {
		t.Fatalf("baseline key is unstable: %q %q", baseline1, baseline2)
	}
}

func TestEventIntentUsesInjectedBusinessTime(t *testing.T) {
	event, err := NewEventIntent("event-1", "company.paused", "company", "company-1", 2, "2026-09-07T10:00:00+08:00", "cmd-1", json.RawMessage(`{"mode":"drain"}`))
	if err != nil || event.BusinessAt != "2026-09-07T10:00:00+08:00" {
		t.Fatalf("event = %+v %v", event, err)
	}
	if _, err := NewEventIntent("event-2", "company.paused", "company", "company-1", 2, "now", "cmd-2", json.RawMessage(`{}`)); err == nil {
		t.Fatal("implicit wall-clock value was accepted")
	}
}

func FuzzCommandReceiptReplay(f *testing.F) {
	f.Add("sha256:a", "sha256:a")
	f.Add("sha256:a", "sha256:b")
	f.Fuzz(func(t *testing.T, stored, replay string) {
		if stored == "" {
			return
		}
		receipt, err := NewCommandReceipt("cmd", "word", stored, json.RawMessage(`{"ok":true}`))
		if err != nil {
			return
		}
		_, err = receipt.Replay(replay)
		if (strings.TrimSpace(replay) == strings.TrimSpace(stored)) != (err == nil) {
			t.Fatalf("replay equality mismatch stored=%q replay=%q err=%v", stored, replay, err)
		}
	})
}
