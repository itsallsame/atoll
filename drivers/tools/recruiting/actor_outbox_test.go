package recruiting

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestOutboxEventFingerprintBindsIdentityKindAndPayload(t *testing.T) {
	base := outboxEventFingerprint("event-1", "company.added", json.RawMessage(`{"v":1}`))
	if base != outboxEventFingerprint("event-1", "company.added", json.RawMessage(`{"v":1}`)) {
		t.Fatal("same event did not produce a stable ledger fingerprint")
	}
	for _, changed := range []string{
		outboxEventFingerprint("event-2", "company.added", json.RawMessage(`{"v":1}`)),
		outboxEventFingerprint("event-1", "company.updated", json.RawMessage(`{"v":1}`)),
		outboxEventFingerprint("event-1", "company.added", json.RawMessage(`{"v":2}`)),
	} {
		if changed == base {
			t.Fatal("fingerprint did not bind the complete immutable event")
		}
	}
}

func TestOutboxRetryDelayIsBoundedExponential(t *testing.T) {
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 5 * time.Minute}
	for attempt, expected := range want {
		if got := outboxRetryDelay(uint64(attempt)); got != expected {
			t.Fatalf("attempt %d delay=%s want=%s", attempt, got, expected)
		}
	}
	if got := outboxRetryDelay(100); got != 5*time.Minute {
		t.Fatalf("retry delay is not capped: %s", got)
	}
}

func TestOutboxErrorClassificationDoesNotPersistErrorText(t *testing.T) {
	if got := classifyOutboxDeliveryError(errors.New("secret endpoint and credentials")); got != "atoll_ledger_unavailable" {
		t.Fatalf("error class=%q", got)
	}
}
