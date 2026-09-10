package model

import (
	"fmt"
	"strings"
	"time"
)

type ProfileRepairSessionStatus string

const (
	ProfileRepairAwaitingDevice ProfileRepairSessionStatus = "awaiting_device"
	ProfileRepairActive         ProfileRepairSessionStatus = "active"
	ProfileRepairSubmitted      ProfileRepairSessionStatus = "submitted"
	ProfileRepairVerified       ProfileRepairSessionStatus = "verified"
	ProfileRepairFailed         ProfileRepairSessionStatus = "failed"
	ProfileRepairExpired        ProfileRepairSessionStatus = "expired"
)

// ProfileRepairSession is durable control-plane authority for one interactive
// repair. It carries only identities and expiry; browser credentials remain on
// the authorized device behind the Profile's opaque secret reference.
type ProfileRepairSession struct {
	SessionID        string                     `json:"session_id"`
	ProfileID        string                     `json:"profile_id"`
	ProfileVersion   uint64                     `json:"profile_version"`
	IncidentID       string                     `json:"repair_incident_id"`
	WorkID           string                     `json:"work_id"`
	RequestedBy      string                     `json:"requested_by"`
	DeviceActorID    string                     `json:"device_actor_id"`
	AttemptID        string                     `json:"attempt_id,omitempty"`
	ValidationWorkID string                     `json:"validation_work_id,omitempty"`
	Status           ProfileRepairSessionStatus `json:"status"`
	ExpiresAt        string                     `json:"expires_at"`
	Version          uint64                     `json:"version"`
}

func NewProfileRepairSession(id, profileID string, profileVersion uint64, incidentID, workID,
	requestedBy, deviceActorID string, expiresAt time.Time) (ProfileRepairSession, error) {
	session := ProfileRepairSession{SessionID: strings.TrimSpace(id), ProfileID: strings.TrimSpace(profileID),
		ProfileVersion: profileVersion, IncidentID: strings.TrimSpace(incidentID), WorkID: strings.TrimSpace(workID),
		RequestedBy: strings.TrimSpace(requestedBy), DeviceActorID: strings.TrimSpace(deviceActorID),
		Status: ProfileRepairAwaitingDevice, ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano), Version: 1}
	if session.SessionID == "" || session.ProfileID == "" || session.ProfileVersion < 2 || session.IncidentID == "" ||
		session.WorkID == "" || session.RequestedBy == "" || session.DeviceActorID == "" || expiresAt.IsZero() {
		return ProfileRepairSession{}, fmt.Errorf("Profile repair session requires complete identity, authority, and expiry")
	}
	return session, nil
}

func (s ProfileRepairSession) Expiry() (time.Time, error) {
	expiresAt, err := time.Parse(time.RFC3339Nano, s.ExpiresAt)
	if err != nil || expiresAt.IsZero() {
		return time.Time{}, fmt.Errorf("Profile repair session expiry is invalid")
	}
	return expiresAt.UTC(), nil
}

func (s ProfileRepairSession) Activate(expected uint64, attemptID, deviceActorID string, at time.Time) (ProfileRepairSession, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return ProfileRepairSession{}, err
	}
	expiresAt, err := s.Expiry()
	if err != nil {
		return ProfileRepairSession{}, err
	}
	attemptID, deviceActorID = strings.TrimSpace(attemptID), strings.TrimSpace(deviceActorID)
	if s.Status != ProfileRepairAwaitingDevice || attemptID == "" || deviceActorID != s.DeviceActorID || at.IsZero() || !at.Before(expiresAt) {
		return ProfileRepairSession{}, &InvalidTransitionError{Entity: "profile repair session", From: string(s.Status), Action: "activate"}
	}
	s.AttemptID, s.Status, s.Version = attemptID, ProfileRepairActive, s.Version+1
	return s, nil
}

func (s ProfileRepairSession) Submit(expected uint64, attemptID, deviceActorID, validationWorkID string,
	at time.Time) (ProfileRepairSession, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return ProfileRepairSession{}, err
	}
	expiresAt, err := s.Expiry()
	if err != nil {
		return ProfileRepairSession{}, err
	}
	if s.Status != ProfileRepairActive || strings.TrimSpace(attemptID) == "" || attemptID != s.AttemptID ||
		strings.TrimSpace(deviceActorID) != s.DeviceActorID || strings.TrimSpace(validationWorkID) == "" ||
		at.IsZero() || !at.Before(expiresAt) {
		return ProfileRepairSession{}, &InvalidTransitionError{Entity: "profile repair session", From: string(s.Status), Action: "submit"}
	}
	s.ValidationWorkID, s.Status, s.Version = strings.TrimSpace(validationWorkID), ProfileRepairSubmitted, s.Version+1
	return s, nil
}

func (s ProfileRepairSession) Expire(expected uint64, at time.Time) (ProfileRepairSession, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return ProfileRepairSession{}, err
	}
	expiresAt, err := s.Expiry()
	if err != nil {
		return ProfileRepairSession{}, err
	}
	if (s.Status != ProfileRepairAwaitingDevice && s.Status != ProfileRepairActive) || at.IsZero() || at.Before(expiresAt) {
		return ProfileRepairSession{}, &InvalidTransitionError{Entity: "profile repair session", From: string(s.Status), Action: "expire"}
	}
	s.Status, s.Version = ProfileRepairExpired, s.Version+1
	return s, nil
}

func (s ProfileRepairSession) Verify(expected uint64, validationWorkID, deviceActorID string) (ProfileRepairSession, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return ProfileRepairSession{}, err
	}
	if s.Status != ProfileRepairSubmitted || strings.TrimSpace(validationWorkID) == "" ||
		validationWorkID != s.ValidationWorkID || strings.TrimSpace(deviceActorID) != s.DeviceActorID {
		return ProfileRepairSession{}, &InvalidTransitionError{Entity: "profile repair session", From: string(s.Status), Action: "verify"}
	}
	s.Status, s.Version = ProfileRepairVerified, s.Version+1
	return s, nil
}

func (s ProfileRepairSession) Fail(expected uint64, attemptID, deviceActorID string) (ProfileRepairSession, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return ProfileRepairSession{}, err
	}
	if (s.Status != ProfileRepairActive && s.Status != ProfileRepairSubmitted) ||
		strings.TrimSpace(attemptID) == "" ||
		strings.TrimSpace(deviceActorID) != s.DeviceActorID {
		return ProfileRepairSession{}, &InvalidTransitionError{Entity: "profile repair session", From: string(s.Status), Action: "fail"}
	}
	// The verification Work owns a distinct Attempt from the interactive Work.
	// Bind the failed Attempt only when failing the interactive phase; verification
	// remains tied by ValidationWorkID and the authenticated device identity.
	if s.Status == ProfileRepairActive && attemptID != s.AttemptID {
		return ProfileRepairSession{}, &InvalidTransitionError{Entity: "profile repair session", From: string(s.Status), Action: "fail"}
	}
	s.Status, s.Version = ProfileRepairFailed, s.Version+1
	return s, nil
}

// RetryAfterAttemptExpiry releases a crashed executor incarnation without
// extending the one-time session deadline. A replacement Attempt must still
// be claimed by the same authorized device before the original expiry.
func (s ProfileRepairSession) RetryAfterAttemptExpiry(expected uint64, attemptID string) (ProfileRepairSession, error) {
	if err := requireVersion(expected, s.Version); err != nil {
		return ProfileRepairSession{}, err
	}
	if s.Status != ProfileRepairActive || strings.TrimSpace(attemptID) == "" || attemptID != s.AttemptID {
		return ProfileRepairSession{}, &InvalidTransitionError{Entity: "profile repair session", From: string(s.Status), Action: "retry after Attempt expiry"}
	}
	s.AttemptID, s.Status, s.Version = "", ProfileRepairAwaitingDevice, s.Version+1
	return s, nil
}
