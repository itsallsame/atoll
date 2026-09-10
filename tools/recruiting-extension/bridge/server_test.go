package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestBridgeAcceptsOnlyPairedChromeExtensionAndReturnsBoundedResult(t *testing.T) {
	fake := &submissionFake{source: readyBridgeSource(), sender: "human:operator:7"}
	service := &Service{Client: fake, Token: strings.Repeat("pairing-secret-", 3), Now: func() time.Time {
		return time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	}}
	handler, err := service.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	wsBase := "ws" + strings.TrimPrefix(server.URL, "http") + "/capture"
	headers := http.Header{"Origin": []string{"chrome-extension://abcdefghijklmnop"}}
	if conn, response, err := websocket.DefaultDialer.Dial(wsBase+"?token=wrong", headers); err == nil {
		_ = conn.Close()
		t.Fatal("bridge accepted a wrong pairing token")
	} else if response == nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong token status=%v err=%v", response, err)
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsBase+"?token="+service.Token, headers)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	draft, _ := json.Marshal(bridgeDraft())
	if err := conn.WriteJSON(map[string]any{"version": BridgeProtocolVersion, "kind": "capture.propose",
		"request_id": "browser-request-1", "draft": json.RawMessage(draft)}); err != nil {
		t.Fatal(err)
	}
	var response bridgeResponse
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "completed" || response.Result == nil || response.Result.CaptureID != "capture-01" ||
		response.RequestID != "browser-request-1" || response.Error != "" {
		t.Fatalf("bridge response=%+v", response)
	}
}

func TestProfileTaskUsesSeparateExecutorTokenAndRelaysOnlyThroughPairedExtension(t *testing.T) {
	now := time.Date(2026, 9, 10, 15, 0, 0, 0, time.UTC)
	service := &Service{Client: &submissionFake{source: readyBridgeSource(), sender: "human:operator:7"},
		Token: strings.Repeat("extension-token-", 3), ExecutorToken: strings.Repeat("executor-token-", 3),
		Now: func() time.Time { return now }}
	handler, err := service.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	canaryRecipe, canaryContentHash, canaryContractHash := testProfileCanaryRecipe(t)
	task := profileTask{Version: browserBrokerProtocolVersion, RequestID: "profile-attempt-1", Kind: "profile_repair",
		SessionID: "session-1", ProfileID: "profile-1", SecurityDomain: "jobs.example.test",
		ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), CanaryURL: "https://jobs.example.test/private/canary",
		CanaryMinimum: 1, CanaryRecipe: canaryRecipe, CanaryRecipeID: "canary-1", CanaryRecipeVersion: 2,
		CanaryContentHash: canaryContentHash, CanaryContractHash: canaryContractHash}
	raw, _ := json.Marshal(task)
	wrong, _ := http.NewRequest(http.MethodPost, server.URL+"/profile-task", bytes.NewReader(raw))
	wrong.Header.Set("Authorization", "Bearer "+service.Token)
	wrongResponse, err := http.DefaultClient.Do(wrong)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, wrongResponse.Body)
	_ = wrongResponse.Body.Close()
	if wrongResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("extension pairing token accessed executor endpoint: %d", wrongResponse.StatusCode)
	}

	resultChannel := make(chan profileResult, 1)
	errorChannel := make(chan error, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/profile-task", bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+service.ExecutorToken)
		response, callErr := http.DefaultClient.Do(request)
		if callErr != nil {
			errorChannel <- callErr
			return
		}
		defer response.Body.Close()
		var result profileResult
		if response.StatusCode != http.StatusOK {
			errorChannel <- fmt.Errorf("Profile task HTTP status %d", response.StatusCode)
			return
		}
		if decodeErr := json.NewDecoder(response.Body).Decode(&result); decodeErr != nil {
			errorChannel <- decodeErr
			return
		}
		resultChannel <- result
	}()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/capture?token=" + service.Token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL,
		http.Header{"Origin": []string{"chrome-extension://abcdefghijklmnop"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var delivery struct {
		Version string      `json:"version"`
		Kind    string      `json:"kind"`
		Task    profileTask `json:"task"`
	}
	if err := conn.ReadJSON(&delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.Version != BridgeProtocolVersion || delivery.Kind != "profile.task" ||
		delivery.Task.RequestID != task.RequestID || delivery.Task.CanaryURL != task.CanaryURL ||
		delivery.Task.CanaryMinimum != task.CanaryMinimum || !bytes.Equal(delivery.Task.CanaryRecipe, task.CanaryRecipe) {
		t.Fatalf("Profile task delivery=%+v", delivery)
	}
	extensionResult := extensionProfileResult{Version: BridgeProtocolVersion, Kind: "profile.result",
		RequestID: task.RequestID, Status: "completed", Evidence: profileEvidence{Version: browserBrokerProtocolVersion,
			TaskKind: task.Kind, SecurityDomain: task.SecurityDomain, ObservedAt: now.Format(time.RFC3339Nano),
			Probe: "interactive_login_completed", CanaryRecipeID: task.CanaryRecipeID,
			CanaryRecipeVersion: task.CanaryRecipeVersion, CanaryContentHash: task.CanaryContentHash,
			CanaryContractHash: task.CanaryContractHash, RecordCount: 1}}
	if err := conn.WriteJSON(extensionResult); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-resultChannel:
		if result.Status != "completed" || result.RequestID != task.RequestID ||
			result.NextSecretRef != "secret://chrome-profile/profile-1/session-1" || result.Authenticated {
			t.Fatalf("Profile broker result=%+v", result)
		}
	case err := <-errorChannel:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Profile broker result")
	}
}

func TestBridgeRejectsReusedPairingTokenForExecutorEndpoint(t *testing.T) {
	token := strings.Repeat("same-token-", 4)
	service := &Service{Client: &submissionFake{source: readyBridgeSource(), sender: "human:operator:7"},
		Token: token, ExecutorToken: token, Now: time.Now}
	if _, err := service.Handler(); err == nil {
		t.Fatal("bridge accepted the extension pairing token as its executor credential")
	}
}

func TestFailedProfileProbeMayReportFewerThanMinimumRecords(t *testing.T) {
	task := profileTask{Kind: "profile_verification", SecurityDomain: "jobs.example.test", CanaryMinimum: 2,
		CanaryRecipeID: "canary-1", CanaryRecipeVersion: 2, CanaryContentHash: "sha256:" + strings.Repeat("a", 64),
		CanaryContractHash: "sha256:" + strings.Repeat("b", 64)}
	result := extensionProfileResult{Status: "failed", FailureCode: "authenticated_canary_failed",
		Evidence: profileEvidence{Version: browserBrokerProtocolVersion, TaskKind: task.Kind,
			SecurityDomain: task.SecurityDomain, Probe: "failed", CanaryRecipeID: task.CanaryRecipeID,
			CanaryRecipeVersion: task.CanaryRecipeVersion, CanaryContentHash: task.CanaryContentHash,
			CanaryContractHash: task.CanaryContractHash, RecordCount: 1}}
	if !validExtensionProfileResult(task, result) {
		t.Fatal("bridge rejected a structurally valid negative canary result")
	}
	result.Status = "completed"
	result.FailureCode = ""
	result.Authenticated = true
	result.Evidence.Probe = "authenticated_canary"
	if validExtensionProfileResult(task, result) {
		t.Fatal("bridge accepted a successful canary below its minimum record proof")
	}
}

func TestProfileTasksAreQueuedAndDeliveredOneAtATime(t *testing.T) {
	now := time.Date(2026, 9, 10, 16, 0, 0, 0, time.UTC)
	service := &Service{Client: &submissionFake{source: readyBridgeSource(), sender: "human:operator:7"},
		Token: strings.Repeat("extension-queue-token-", 2), ExecutorToken: strings.Repeat("executor-queue-token-", 2),
		Now: func() time.Time { return now }}
	handler, err := service.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	canaryRecipe, canaryContentHash, canaryContractHash := testProfileCanaryRecipe(t)
	base := profileTask{Version: browserBrokerProtocolVersion, Kind: "profile_verification", ProfileID: "profile-1",
		SecurityDomain: "jobs.example.test", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		CanaryURL: "https://jobs.example.test/private/canary", CanaryMinimum: 1,
		CanaryRecipe: canaryRecipe, CanaryRecipeID: "canary-1", CanaryRecipeVersion: 1,
		CanaryContentHash: canaryContentHash, CanaryContractHash: canaryContractHash}
	results := make(chan profileResult, 2)
	errors := make(chan error, 2)
	post := func(task profileTask) {
		raw, _ := json.Marshal(task)
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/profile-task", bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+service.ExecutorToken)
		response, callErr := http.DefaultClient.Do(request)
		if callErr != nil {
			errors <- callErr
			return
		}
		defer response.Body.Close()
		var result profileResult
		if response.StatusCode != http.StatusOK {
			errors <- fmt.Errorf("Profile queue HTTP status %d", response.StatusCode)
			return
		}
		if decodeErr := json.NewDecoder(response.Body).Decode(&result); decodeErr != nil {
			errors <- decodeErr
			return
		}
		results <- result
	}
	first := base
	first.RequestID, first.SessionID = "queue-attempt-1", "queue-session-1"
	go post(first)
	waitForPendingProfileTasks(t, service, 1)
	second := base
	second.RequestID, second.SessionID = "queue-attempt-2", "queue-session-2"
	go post(second)
	waitForPendingProfileTasks(t, service, 2)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/capture?token=" + service.Token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL,
		http.Header{"Origin": []string{"chrome-extension://abcdefghijklmnop"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	readTask := func() profileTask {
		var delivery struct {
			Task profileTask `json:"task"`
		}
		if err := conn.ReadJSON(&delivery); err != nil {
			t.Fatal(err)
		}
		return delivery.Task
	}
	if delivered := readTask(); delivered.RequestID != first.RequestID {
		t.Fatalf("first queued delivery=%s want=%s", delivered.RequestID, first.RequestID)
	}
	service.profileMu.Lock()
	active, queued := service.profileActive, append([]string(nil), service.profileOrder...)
	service.profileMu.Unlock()
	if active != first.RequestID || len(queued) != 2 || queued[1] != second.RequestID {
		t.Fatalf("Profile queue active=%q order=%v", active, queued)
	}
	writeSuccessfulVerificationResult(t, conn, first, now)
	if delivered := readTask(); delivered.RequestID != second.RequestID {
		t.Fatalf("second queued delivery=%s want=%s", delivered.RequestID, second.RequestID)
	}
	writeSuccessfulVerificationResult(t, conn, second, now)
	for range 2 {
		select {
		case result := <-results:
			if result.Status != "completed" {
				t.Fatalf("queued Profile result=%+v", result)
			}
		case err := <-errors:
			t.Fatal(err)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for queued Profile result")
		}
	}
}

func waitForPendingProfileTasks(t *testing.T, service *Service, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		service.profileMu.Lock()
		actual := len(service.profileTasks)
		service.profileMu.Unlock()
		if actual == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Profile queue did not reach %d pending tasks", count)
}

func testProfileCanaryRecipe(t *testing.T) (json.RawMessage, string, string) {
	t.Helper()
	spec := recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail,
		RequiredCapability: "browser.profile.repair", Transport: recipeabi.TransportBrowser,
		Request: recipeabi.ReadRequest{Method: "GET", TimeoutMS: 10_000, MaxResponseBytes: 1 << 20,
			MaxRedirects: 1, UserAgent: "Atoll-Profile-Test/1"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{"account": "#account"}}}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	contentHash, err := spec.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	contractHash, err := spec.ContractHash()
	if err != nil {
		t.Fatal(err)
	}
	return raw, contentHash, contractHash
}

func writeSuccessfulVerificationResult(t *testing.T, conn *websocket.Conn, task profileTask, now time.Time) {
	t.Helper()
	result := extensionProfileResult{Version: BridgeProtocolVersion, Kind: "profile.result", RequestID: task.RequestID,
		Status: "completed", Authenticated: true, Evidence: profileEvidence{Version: browserBrokerProtocolVersion,
			TaskKind: task.Kind, SecurityDomain: task.SecurityDomain, ObservedAt: now.Format(time.RFC3339Nano),
			Probe: "authenticated_canary", CanaryRecipeID: task.CanaryRecipeID,
			CanaryRecipeVersion: task.CanaryRecipeVersion, CanaryContentHash: task.CanaryContentHash,
			CanaryContractHash: task.CanaryContractHash, RecordCount: 1}}
	if err := conn.WriteJSON(result); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeRejectsNonExtensionOriginAndUnknownWireFields(t *testing.T) {
	service := &Service{Client: &submissionFake{source: readyBridgeSource(), sender: "human:operator:7"},
		Token: strings.Repeat("strong-token", 4), Now: time.Now}
	handler, _ := service.Handler()
	server := httptest.NewServer(handler)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/capture?token=" + service.Token
	if conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": []string{"https://evil.example"}}); err == nil {
		_ = conn.Close()
		t.Fatal("non-extension Origin connected to bridge")
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Origin": []string{"chrome-extension://abcdefghijklmnop"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.WriteJSON(map[string]any{"version": BridgeProtocolVersion, "kind": "capture.propose",
		"request_id": "bad-request", "draft": bridgeDraft(), "publish": true})
	var response bridgeResponse
	if err := conn.ReadJSON(&response); err != nil || response.Status != "failed" || response.Error != "invalid_bridge_request" {
		t.Fatalf("unknown bridge field response=%+v err=%v", response, err)
	}
}
