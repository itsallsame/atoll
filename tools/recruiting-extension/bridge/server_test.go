package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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
