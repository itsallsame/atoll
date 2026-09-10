package model

import (
	"testing"
	"time"
)

func TestProfileRepairSessionIsDeviceBoundOneTimeAuthority(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	session, err := NewProfileRepairSession("session-1", "profile-1", 2, "incident-1", "work-1",
		"human:operator", "tool:authorized-device", now.Add(10*time.Minute))
	if err != nil || session.Status != ProfileRepairAwaitingDevice || session.Version != 1 {
		t.Fatalf("new session=%+v err=%v", session, err)
	}
	if _, err := session.Activate(session.Version, "attempt-1", "tool:other-device", now); err == nil {
		t.Fatal("wrong device activated Profile repair session")
	}
	active, err := session.Activate(session.Version, "attempt-1", session.DeviceActorID, now)
	if err != nil || active.Status != ProfileRepairActive || active.Version != 2 {
		t.Fatalf("active session=%+v err=%v", active, err)
	}
	if _, err := active.Submit(active.Version, "attempt-2", active.DeviceActorID, "validation-work", now); err == nil {
		t.Fatal("different Attempt submitted Profile repair session")
	}
	submitted, err := active.Submit(active.Version, active.AttemptID, active.DeviceActorID, "validation-work", now)
	if err != nil || submitted.Status != ProfileRepairSubmitted || submitted.Version != 3 {
		t.Fatalf("submitted session=%+v err=%v", submitted, err)
	}
	verified, err := submitted.Verify(submitted.Version, submitted.ValidationWorkID, submitted.DeviceActorID)
	if err != nil || verified.Status != ProfileRepairVerified || verified.Version != 4 {
		t.Fatalf("verified session=%+v err=%v", verified, err)
	}
	if _, err := submitted.Submit(submitted.Version, submitted.AttemptID, submitted.DeviceActorID, "validation-work-2", now); err == nil {
		t.Fatal("Profile repair session was submitted twice")
	}
	failed, err := submitted.Fail(submitted.Version, "verification-attempt", submitted.DeviceActorID)
	if err != nil || failed.Status != ProfileRepairFailed || failed.Version != submitted.Version+1 {
		t.Fatalf("failed submitted session=%+v err=%v", failed, err)
	}
	failedActive, err := active.Fail(active.Version, active.AttemptID, active.DeviceActorID)
	if err != nil || failedActive.Status != ProfileRepairFailed {
		t.Fatalf("failed active session=%+v err=%v", failedActive, err)
	}
	retryable, err := active.RetryAfterAttemptExpiry(active.Version, active.AttemptID)
	if err != nil || retryable.Status != ProfileRepairAwaitingDevice || retryable.AttemptID != "" ||
		retryable.Version != active.Version+1 || retryable.ExpiresAt != active.ExpiresAt {
		t.Fatalf("retryable crashed session=%+v err=%v", retryable, err)
	}
}

func TestProfileRepairSessionExpiresBeforeActivationOrSubmission(t *testing.T) {
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	session, _ := NewProfileRepairSession("session-expiry", "profile-1", 2, "incident-1", "work-1",
		"human:operator", "tool:authorized-device", now.Add(time.Minute))
	if _, err := session.Activate(session.Version, "attempt-1", session.DeviceActorID, now.Add(time.Minute)); err == nil {
		t.Fatal("expired session was activated")
	}
	expired, err := session.Expire(session.Version, now.Add(time.Minute))
	if err != nil || expired.Status != ProfileRepairExpired || expired.Version != 2 {
		t.Fatalf("expired session=%+v err=%v", expired, err)
	}
	activeSession, _ := NewProfileRepairSession("session-active-expiry", "profile-1", 2, "incident-1", "work-2",
		"human:operator", "tool:authorized-device", now.Add(time.Minute))
	active, _ := activeSession.Activate(activeSession.Version, "attempt-2", activeSession.DeviceActorID, now)
	if _, err := active.Submit(active.Version, active.AttemptID, active.DeviceActorID, "validation-work", now.Add(time.Minute)); err == nil {
		t.Fatal("expired active session accepted submission")
	}
}
