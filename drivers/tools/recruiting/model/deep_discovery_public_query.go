package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"strings"
)

type PublicQueryRequestEvidence struct {
	EndpointURL string            `json:"endpoint_url"`
	Method      string            `json:"method"`
	Headers     map[string]string `json:"headers"`
	JSONBody    json.RawMessage   `json:"json_body"`
	BodyHash    string            `json:"body_hash"`
}

func (e PublicQueryRequestEvidence) Matches(other PublicQueryRequestEvidence) bool {
	if e.EndpointURL != other.EndpointURL || e.Method != other.Method || e.BodyHash != other.BodyHash ||
		!bytes.Equal(e.JSONBody, other.JSONBody) || len(e.Headers) != len(other.Headers) {
		return false
	}
	for name, value := range e.Headers {
		if other.Headers[name] != value {
			return false
		}
	}
	return true
}

type DeepDiscoveryPublicQueryStatus string

const (
	DeepDiscoveryPublicQueryQueued    DeepDiscoveryPublicQueryStatus = "queued"
	DeepDiscoveryPublicQueryCompleted DeepDiscoveryPublicQueryStatus = "completed"
)

// DeepDiscoveryPublicQueryVerification binds one browser-observed request to
// an independently fetched response Artifact. It is immutable after terminal
// completion and therefore safe to reference from a later Browser Probe.
type DeepDiscoveryPublicQueryVerification struct {
	VerificationID string                         `json:"verification_id"`
	MissionID      string                         `json:"mission_id"`
	MissionVersion uint64                         `json:"mission_version"`
	ProbeID        string                         `json:"probe_id"`
	WorkID         string                         `json:"work_id"`
	Request        PublicQueryRequestEvidence     `json:"request"`
	Status         DeepDiscoveryPublicQueryStatus `json:"status"`
	Artifact       *ArtifactMetadata              `json:"artifact,omitempty"`
	StatusCode     int                            `json:"status_code,omitempty"`
	ContentType    string                         `json:"content_type,omitempty"`
	Version        uint64                         `json:"version"`
}

func NewDeepDiscoveryPublicQueryVerification(id, missionID, probeID, workID string, missionVersion uint64,
	request PublicQueryRequestEvidence) (DeepDiscoveryPublicQueryVerification, error) {
	id, missionID, probeID, workID = strings.TrimSpace(id), strings.TrimSpace(missionID), strings.TrimSpace(probeID), strings.TrimSpace(workID)
	if id == "" || missionID == "" || probeID == "" || workID == "" || missionVersion == 0 ||
		len(id) > 191 || len(missionID) > 191 || len(probeID) > 191 || len(workID) > 191 {
		return DeepDiscoveryPublicQueryVerification{}, fmt.Errorf("public query verification requires bounded lineage and Work identity")
	}
	if request.EndpointURL == "" || request.Method != "POST" || len(request.JSONBody) == 0 || request.BodyHash == "" {
		return DeepDiscoveryPublicQueryVerification{}, fmt.Errorf("public query verification requires complete frozen request evidence")
	}
	return DeepDiscoveryPublicQueryVerification{VerificationID: id, MissionID: missionID, MissionVersion: missionVersion,
		ProbeID: probeID, WorkID: workID, Request: request, Status: DeepDiscoveryPublicQueryQueued, Version: 1}, nil
}

func (v DeepDiscoveryPublicQueryVerification) Complete(expected uint64, artifact ArtifactMetadata,
	statusCode int, contentType string) (DeepDiscoveryPublicQueryVerification, error) {
	mediaType, _, mediaErr := mime.ParseMediaType(strings.TrimSpace(contentType))
	if expected != v.Version || v.Status != DeepDiscoveryPublicQueryQueued || artifact.ArtifactID == "" ||
		artifact.WorkID != v.WorkID || artifact.Kind != ArtifactResponse || statusCode < 200 || statusCode > 299 ||
		mediaErr != nil || mediaType != "application/json" {
		return DeepDiscoveryPublicQueryVerification{}, fmt.Errorf("queued public query verification requires one bound successful JSON response Artifact")
	}
	v.Status = DeepDiscoveryPublicQueryCompleted
	v.Artifact = &artifact
	v.StatusCode = statusCode
	v.ContentType = strings.TrimSpace(contentType)
	v.Version++
	return v, nil
}
