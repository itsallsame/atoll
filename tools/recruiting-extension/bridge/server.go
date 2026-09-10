package bridge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

const BridgeProtocolVersion = "recruiting.extension-bridge.v1"

type Service struct {
	Client        atollSubmissionClient
	Token         string
	ExecutorToken string
	Now           func() time.Time
	submissionMu  sync.Mutex
	profileMu     sync.Mutex
	profileConn   *profileConnection
	profileTasks  map[string]*pendingProfileTask
	profileOrder  []string
	profileActive string
}

type profileConnection struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

type profileTask struct {
	Version             string          `json:"version"`
	RequestID           string          `json:"request_id"`
	Kind                string          `json:"kind"`
	SessionID           string          `json:"session_id"`
	ProfileID           string          `json:"profile_id"`
	SecurityDomain      string          `json:"security_domain"`
	ExpiresAt           string          `json:"expires_at"`
	CanaryURL           string          `json:"canary_url"`
	CanaryMinimum       int             `json:"canary_minimum_records"`
	CanaryRecipe        json.RawMessage `json:"canary_recipe"`
	CanaryRecipeID      string          `json:"canary_recipe_id"`
	CanaryRecipeVersion uint64          `json:"canary_recipe_version"`
	CanaryContentHash   string          `json:"canary_content_hash"`
	CanaryContractHash  string          `json:"canary_contract_hash"`
}

type profileEvidence struct {
	Version             string `json:"version"`
	TaskKind            string `json:"task_kind"`
	SecurityDomain      string `json:"security_domain"`
	ObservedAt          string `json:"observed_at"`
	Probe               string `json:"probe"`
	CanaryRecipeID      string `json:"canary_recipe_id"`
	CanaryRecipeVersion uint64 `json:"canary_recipe_version"`
	CanaryContentHash   string `json:"canary_content_hash"`
	CanaryContractHash  string `json:"canary_contract_hash"`
	RecordCount         int    `json:"record_count"`
}

type profileResult struct {
	Version       string          `json:"version"`
	RequestID     string          `json:"request_id"`
	Status        string          `json:"status"`
	NextSecretRef string          `json:"next_secret_ref,omitempty"`
	Authenticated bool            `json:"authenticated,omitempty"`
	Evidence      profileEvidence `json:"evidence"`
	FailureCode   string          `json:"failure_code,omitempty"`
}

type extensionProfileResult struct {
	Version       string          `json:"version"`
	Kind          string          `json:"kind"`
	RequestID     string          `json:"request_id"`
	Status        string          `json:"status"`
	Authenticated bool            `json:"authenticated,omitempty"`
	Evidence      profileEvidence `json:"evidence"`
	FailureCode   string          `json:"failure_code,omitempty"`
}

type pendingProfileTask struct {
	task   profileTask
	result chan profileResult
}

const maxPendingProfileTasks = 8

type bridgeRequest struct {
	Version   string          `json:"version"`
	Kind      string          `json:"kind"`
	RequestID string          `json:"request_id"`
	Draft     json.RawMessage `json:"draft"`
}

type bridgeResponse struct {
	Version   string            `json:"version"`
	RequestID string            `json:"request_id"`
	Status    string            `json:"status"`
	Result    *SubmissionResult `json:"result,omitempty"`
	Error     string            `json:"error,omitempty"`
}

func (s *Service) Handler() (http.Handler, error) {
	if s == nil || s.Client == nil || len(s.Token) < 32 || s.Now == nil ||
		(s.ExecutorToken != "" && (len(s.ExecutorToken) < 32 ||
			subtle.ConstantTimeCompare([]byte(s.ExecutorToken), []byte(s.Token)) == 1)) {
		return nil, fmt.Errorf("bridge service requires Atoll client, strong pairing token, and clock")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ready"}`))
	})
	if s.ExecutorToken != "" {
		mux.HandleFunc("POST /profile-task", s.handleProfileTaskHTTP)
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(request *http.Request) bool {
		origin := request.Header.Get("Origin")
		parsed, err := url.Parse(origin)
		return err == nil && parsed.Scheme == "chrome-extension" && parsed.Host != ""
	}}
	mux.HandleFunc("GET /capture", func(writer http.ResponseWriter, request *http.Request) {
		if !loopbackRemote(request.RemoteAddr) || subtle.ConstantTimeCompare([]byte(request.URL.Query().Get("token")), []byte(s.Token)) != 1 {
			http.Error(writer, "not found", http.StatusNotFound)
			return
		}
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := &profileConnection{conn: conn}
		s.attachProfileConnection(connection)
		defer s.detachProfileConnection(connection)
		conn.SetReadLimit(extensionDraftWireLimit)
		for {
			_, raw, readErr := conn.ReadMessage()
			if readErr != nil {
				return
			}
			if s.handleExtensionProfileResult(raw) {
				continue
			}
			response := s.handle(request.Context(), raw)
			if writeErr := connection.writeJSON(response); writeErr != nil {
				return
			}
		}
	})
	return mux, nil
}

const extensionDraftWireLimit = 2 << 20

func (s *Service) handle(ctx context.Context, raw []byte) bridgeResponse {
	var request bridgeRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&request)
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	if decodeErr != nil || !errors.Is(trailingErr, io.EOF) || request.Version != BridgeProtocolVersion ||
		request.Kind != "capture.propose" || strings.TrimSpace(request.RequestID) == "" || len(request.RequestID) > 128 {
		return bridgeResponse{Version: BridgeProtocolVersion, RequestID: request.RequestID,
			Status: "failed", Error: "invalid_bridge_request"}
	}
	draft, err := DecodeDraft(request.Draft)
	if err != nil {
		return bridgeResponse{Version: BridgeProtocolVersion, RequestID: request.RequestID,
			Status: "failed", Error: err.Error()}
	}
	s.submissionMu.Lock()
	defer s.submissionMu.Unlock()
	result, err := SubmitDraft(ctx, s.Client, draft, s.Now)
	if err != nil {
		return bridgeResponse{Version: BridgeProtocolVersion, RequestID: request.RequestID,
			Status: "failed", Error: err.Error()}
	}
	return bridgeResponse{Version: BridgeProtocolVersion, RequestID: request.RequestID,
		Status: "completed", Result: &result}
}

func (c *profileConnection) writeJSON(value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteJSON(value)
}

func (s *Service) attachProfileConnection(connection *profileConnection) {
	s.profileMu.Lock()
	previous := s.profileConn
	s.profileConn = connection
	if s.profileTasks == nil {
		s.profileTasks = make(map[string]*pendingProfileTask)
	}
	var task profileTask
	deliver := false
	if pending := s.profileTasks[s.profileActive]; s.profileActive != "" && pending != nil {
		task, deliver = pending.task, true
	} else {
		s.profileActive = ""
		task, deliver = s.nextProfileDeliveryLocked()
	}
	s.profileMu.Unlock()
	if previous != nil && previous != connection {
		_ = previous.conn.Close()
	}
	if deliver {
		s.deliverProfileTask(connection, task)
	}
}

func (s *Service) detachProfileConnection(connection *profileConnection) {
	s.profileMu.Lock()
	if s.profileConn == connection {
		s.profileConn = nil
	}
	s.profileMu.Unlock()
}

func (s *Service) handleProfileTaskHTTP(writer http.ResponseWriter, request *http.Request) {
	if !loopbackRemote(request.RemoteAddr) || !bearerMatches(request.Header.Get("Authorization"), s.ExecutorToken) {
		http.Error(writer, "not found", http.StatusNotFound)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 384<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var task profileTask
	if err := decoder.Decode(&task); err != nil {
		http.Error(writer, "invalid profile task", http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || validateProfileTask(task, s.Now()) != nil {
		http.Error(writer, "invalid profile task", http.StatusBadRequest)
		return
	}
	pending := &pendingProfileTask{task: task, result: make(chan profileResult, 1)}
	s.profileMu.Lock()
	if s.profileTasks == nil {
		s.profileTasks = make(map[string]*pendingProfileTask)
	}
	if _, exists := s.profileTasks[task.RequestID]; exists {
		s.profileMu.Unlock()
		http.Error(writer, "profile task already pending", http.StatusConflict)
		return
	}
	if len(s.profileTasks) >= maxPendingProfileTasks {
		s.profileMu.Unlock()
		http.Error(writer, "too many pending profile tasks", http.StatusTooManyRequests)
		return
	}
	s.profileTasks[task.RequestID] = pending
	s.profileOrder = append(s.profileOrder, task.RequestID)
	delivery, deliver := s.nextProfileDeliveryLocked()
	connection := s.profileConn
	s.profileMu.Unlock()
	defer func() {
		s.finishProfileTask(task.RequestID, pending)
	}()
	if connection != nil && deliver {
		s.deliverProfileTask(connection, delivery)
	}
	expiresAt, _ := time.Parse(time.RFC3339Nano, task.ExpiresAt)
	timer := time.NewTimer(expiresAt.Sub(s.Now()))
	defer timer.Stop()
	var result profileResult
	select {
	case result = <-pending.result:
	case <-request.Context().Done():
		return
	case <-timer.C:
		result = failedProfileResult(task, "session_expired", s.Now())
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(result)
}

func (s *Service) handleExtensionProfileResult(raw []byte) bool {
	var header struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &header) != nil || header.Kind != "profile.result" {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result extensionProfileResult
	if decoder.Decode(&result) != nil {
		return true
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) || result.Version != BridgeProtocolVersion || result.RequestID == "" {
		return true
	}
	s.profileMu.Lock()
	pending := s.profileTasks[result.RequestID]
	s.profileMu.Unlock()
	if pending == nil || !validExtensionProfileResult(pending.task, result) {
		return true
	}
	response := profileResult{Version: browserBrokerProtocolVersion, RequestID: result.RequestID,
		Status: result.Status, Authenticated: result.Authenticated, Evidence: result.Evidence, FailureCode: result.FailureCode}
	if pending.task.Kind == "profile_repair" && result.Status == "completed" {
		response.NextSecretRef = "secret://chrome-profile/" + url.PathEscape(pending.task.ProfileID) + "/" + url.PathEscape(pending.task.SessionID)
	}
	select {
	case pending.result <- response:
		s.finishProfileTask(result.RequestID, pending)
	default:
	}
	return true
}

// nextProfileDeliveryLocked reserves the oldest queued task for the single
// interactive browser surface. Caller must hold profileMu.
func (s *Service) nextProfileDeliveryLocked() (profileTask, bool) {
	if s.profileActive != "" || s.profileConn == nil {
		return profileTask{}, false
	}
	for len(s.profileOrder) != 0 {
		requestID := s.profileOrder[0]
		pending := s.profileTasks[requestID]
		if pending != nil {
			s.profileActive = requestID
			return pending.task, true
		}
		s.profileOrder = s.profileOrder[1:]
	}
	return profileTask{}, false
}

func (s *Service) finishProfileTask(requestID string, pending *pendingProfileTask) {
	s.profileMu.Lock()
	if s.profileTasks[requestID] != pending {
		s.profileMu.Unlock()
		return
	}
	delete(s.profileTasks, requestID)
	for index, queuedID := range s.profileOrder {
		if queuedID == requestID {
			s.profileOrder = append(s.profileOrder[:index], s.profileOrder[index+1:]...)
			break
		}
	}
	if s.profileActive == requestID {
		s.profileActive = ""
	}
	next, deliver := s.nextProfileDeliveryLocked()
	connection := s.profileConn
	s.profileMu.Unlock()
	if connection != nil && deliver {
		s.deliverProfileTask(connection, next)
	}
}

func (s *Service) deliverProfileTask(connection *profileConnection, task profileTask) {
	if err := connection.writeJSON(map[string]any{"version": BridgeProtocolVersion, "kind": "profile.task", "task": task}); err != nil {
		s.detachProfileConnection(connection)
	}
}

const browserBrokerProtocolVersion = "recruiting.browser-broker.v1"

func validateProfileTask(task profileTask, now time.Time) error {
	expiresAt, err := time.Parse(time.RFC3339Nano, task.ExpiresAt)
	if err != nil || task.Version != browserBrokerProtocolVersion || strings.TrimSpace(task.RequestID) == "" ||
		len(task.RequestID) > 191 || strings.TrimSpace(task.SessionID) == "" || strings.TrimSpace(task.ProfileID) == "" ||
		(task.Kind != "profile_repair" && task.Kind != "profile_verification") || !now.Before(expiresAt) ||
		task.CanaryMinimum < 1 || task.CanaryMinimum > 100 || len(task.CanaryRecipe) == 0 ||
		len(task.CanaryRecipe) > 256<<10 || !json.Valid(task.CanaryRecipe) || strings.TrimSpace(task.CanaryRecipeID) == "" ||
		task.CanaryRecipeVersion == 0 || !validProfileHash(task.CanaryContentHash) || !validProfileHash(task.CanaryContractHash) {
		return errors.New("invalid Profile task identity or expiry")
	}
	domain, err := url.Parse("https://" + task.SecurityDomain)
	if err != nil || domain.Hostname() != task.SecurityDomain || domain.Port() != "" {
		return errors.New("invalid Profile security domain")
	}
	canary, err := url.Parse(task.CanaryURL)
	if err != nil || canary.Scheme != "https" || canary.Hostname() != task.SecurityDomain || canary.Port() != "" ||
		canary.User != nil || canary.Fragment != "" {
		return errors.New("invalid Profile canary endpoint")
	}
	spec, err := recipeabi.DecodeSpec(task.CanaryRecipe)
	if err != nil || spec.Transport != recipeabi.TransportBrowser ||
		spec.RequiredCapability != "browser.profile.repair" ||
		(spec.Kind != recipeabi.KindListing && spec.Kind != recipeabi.KindDetail) {
		return errors.New("invalid Profile canary Recipe")
	}
	contentHash, contentErr := spec.ContentHash()
	contractHash, contractErr := spec.ContractHash()
	if contentErr != nil || contractErr != nil || contentHash != task.CanaryContentHash || contractHash != task.CanaryContractHash {
		return errors.New("Profile canary Recipe hash mismatch")
	}
	return nil
}

func validProfileHash(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validExtensionProfileResult(task profileTask, result extensionProfileResult) bool {
	if result.Status != "completed" && result.Status != "failed" {
		return false
	}
	if result.Evidence.Version != browserBrokerProtocolVersion || result.Evidence.TaskKind != task.Kind ||
		result.Evidence.SecurityDomain != task.SecurityDomain || result.Evidence.CanaryRecipeID != task.CanaryRecipeID ||
		result.Evidence.CanaryRecipeVersion != task.CanaryRecipeVersion || result.Evidence.CanaryContentHash != task.CanaryContentHash ||
		result.Evidence.CanaryContractHash != task.CanaryContractHash || result.Evidence.RecordCount < 0 || result.Evidence.RecordCount > 100 {
		return false
	}
	if result.Status == "failed" {
		return result.FailureCode != "" && !result.Authenticated && result.Evidence.Probe == "failed" &&
			result.Evidence.RecordCount < task.CanaryMinimum
	}
	if result.FailureCode != "" {
		return false
	}
	if result.Evidence.RecordCount < task.CanaryMinimum {
		return false
	}
	if task.Kind == "profile_repair" {
		return !result.Authenticated && result.Evidence.Probe == "interactive_login_completed"
	}
	return result.Authenticated && result.Evidence.Probe == "authenticated_canary"
}

func failedProfileResult(task profileTask, code string, now time.Time) profileResult {
	return profileResult{Version: browserBrokerProtocolVersion, RequestID: task.RequestID, Status: "failed", FailureCode: code,
		Evidence: profileEvidence{Version: browserBrokerProtocolVersion, TaskKind: task.Kind,
			SecurityDomain: task.SecurityDomain, ObservedAt: now.UTC().Format(time.RFC3339Nano), Probe: "failed"}}
}

func bearerMatches(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), []byte(token)) == 1
}

func loopbackRemote(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}
