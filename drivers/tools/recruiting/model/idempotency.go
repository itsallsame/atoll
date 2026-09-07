package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

type CommandReceipt struct {
	CommandID   string          `json:"command_id"`
	Word        string          `json:"word"`
	RequestHash string          `json:"request_hash"`
	Response    json.RawMessage `json:"response"`
}

func NewCommandReceipt(commandID, word, requestHash string, response json.RawMessage) (CommandReceipt, error) {
	if strings.TrimSpace(commandID) == "" || strings.TrimSpace(word) == "" || strings.TrimSpace(requestHash) == "" || !json.Valid(response) {
		return CommandReceipt{}, fmt.Errorf("command, word, request hash, and valid JSON response are required")
	}
	return CommandReceipt{
		CommandID: strings.TrimSpace(commandID), Word: strings.TrimSpace(word), RequestHash: strings.TrimSpace(requestHash),
		Response: append(json.RawMessage(nil), response...),
	}, nil
}

func (r CommandReceipt) Replay(requestHash string) (json.RawMessage, error) {
	if strings.TrimSpace(requestHash) != r.RequestHash {
		return nil, fmt.Errorf("command ID reused with a different request")
	}
	return append(json.RawMessage(nil), r.Response...), nil
}

func DetailWorkKey(sourceID, sourceJobKey string, refreshGeneration uint64, purpose string) (string, error) {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(sourceJobKey) == "" || refreshGeneration == 0 || strings.TrimSpace(purpose) == "" {
		return "", fmt.Errorf("source, source job key, refresh generation, and purpose are required")
	}
	return fmt.Sprintf("%s|%s|%d|%s", sourceID, sourceJobKey, refreshGeneration, purpose), nil
}

func BaselineWorkKey(sourceID string, baselineGeneration uint64) (string, error) {
	if strings.TrimSpace(sourceID) == "" || baselineGeneration == 0 {
		return "", fmt.Errorf("source and baseline generation are required")
	}
	return fmt.Sprintf("%s|%d", sourceID, baselineGeneration), nil
}
