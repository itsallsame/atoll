package recruitingexecutor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPBrowserBrokerRequiresLoopbackAndProtectedTokenFile(t *testing.T) {
	for _, endpoint := range []string{"https://127.0.0.1:1234", "http://localhost:1234", "http://192.0.2.1:1234", "http://127.0.0.1:1234/path"} {
		if err := validateBrowserBrokerURL(endpoint); err == nil {
			t.Fatalf("unsafe broker endpoint accepted: %s", endpoint)
		}
	}
	path := filepath.Join(t.TempDir(), "broker.token")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 40)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readBrokerTokenFile(path); err == nil {
		t.Fatal("world-readable broker token was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := readBrokerTokenFile(path); err != nil || len(token) != 40 {
		t.Fatalf("protected broker token length=%d err=%v", len(token), err)
	}
}

func TestHTTPBrowserBrokerBindsTokenAndTaskIdentity(t *testing.T) {
	token := strings.Repeat("executor-secret-", 3)
	task := browserProfileTask{Version: browserBrokerProtocolVersion, RequestID: "attempt-a", Kind: "profile_verification",
		SessionID: "session-a", ProfileID: "profile-a", SecurityDomain: "jobs.example.test",
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/profile-task" || request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(writer, "not found", http.StatusNotFound)
			return
		}
		var received browserProfileTask
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil || received.RequestID != task.RequestID ||
			received.CanaryRecipe.ABIVersion != task.CanaryRecipe.ABIVersion {
			http.Error(writer, "bad task", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(writer).Encode(browserProfileResult{Version: browserBrokerProtocolVersion,
			RequestID: task.RequestID, Status: "completed", Authenticated: true,
			Evidence: browserProfileEvidence{Version: browserBrokerProtocolVersion, TaskKind: task.Kind,
				SecurityDomain: task.SecurityDomain, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Probe: "authenticated_canary"}})
	}))
	defer server.Close()
	tokenPath := filepath.Join(t.TempDir(), "broker.token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := newHTTPBrowserBroker(server.URL, tokenPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Execute(context.Background(), task)
	if err != nil || result.RequestID != task.RequestID || !result.Authenticated {
		t.Fatalf("broker result=%+v err=%v", result, err)
	}
}
