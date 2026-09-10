package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestAtollClientUsesOrdinarySessionAndCorrelatesOutOfOrderFeed(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(request *http.Request) bool {
		return request.Header.Get("Origin") == "http://"+request.Host
	}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/identity/login":
			var login map[string]string
			_ = json.NewDecoder(request.Body).Decode(&login)
			if login["email"] != "operator@example.test" || login["password"] != "local-password" {
				http.Error(writer, "bad login", http.StatusUnauthorized)
				return
			}
			http.SetCookie(writer, &http.Cookie{Name: "atoll_session", Value: "ordinary-session", Path: "/", HttpOnly: true})
			_ = json.NewEncoder(writer).Encode(map[string]string{"id": "operator", "home_channel_id": "home-1"})
		case "/ws":
			cookie, err := request.Cookie("atoll_session")
			if err != nil || cookie.Value != "ordinary-session" {
				http.Error(writer, "missing ordinary session", http.StatusUnauthorized)
				return
			}
			conn, err := upgrader.Upgrade(writer, request, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			var attach map[string]any
			_ = conn.ReadJSON(&attach)
			_ = conn.WriteJSON(wireFrame("receipt", attach["ref"].(string), map[string]any{"contract_version": "5"}))
			var submit map[string]any
			_ = conn.ReadJSON(&submit)
			ref := submit["ref"].(string)
			messageID := "request-1"
			requestEnvelope := map[string]any{"id": messageID, "kind": "request",
				"sender": map[string]any{"kind": "human", "id": "human:operator:7"}, "payload": map[string]any{}}
			terminalEnvelope := map[string]any{"id": "response-1", "parent_id": messageID, "kind": "response",
				"sender":  map[string]any{"kind": "tool", "id": "tool:recruiting:1"},
				"payload": map[string]any{"status": "completed", "entity": map[string]any{"source_id": "source-1"}}}
			// Feed-before-receipt is legal; the bridge must not lose identity or terminal correlation.
			_ = conn.WriteJSON(wireFrame("feed", "", map[string]any{"envelope": requestEnvelope}))
			_ = conn.WriteJSON(wireFrame("feed", "", map[string]any{"envelope": terminalEnvelope}))
			_ = conn.WriteJSON(wireFrame("receipt", ref, map[string]any{"message_id": messageID}))
			var resource map[string]any
			_ = conn.ReadJSON(&resource)
			payload := resource["payload"].(map[string]any)
			if payload["resource_id"] != "artifact://capture/evidence" || payload["op"] != "create" {
				t.Errorf("resource frame=%v", resource)
			}
			_ = conn.WriteJSON(wireFrame("receipt", resource["ref"].(string), map[string]any{"status": "ok"}))
			var duplicate map[string]any
			_ = conn.ReadJSON(&duplicate)
			_ = conn.WriteJSON(wireFrame("error", duplicate["ref"].(string), map[string]any{
				"frame": "resource", "code": "conflict_exists", "detail": "already exists"}))
			var read map[string]any
			_ = conn.ReadJSON(&read)
			readPayload := read["payload"].(map[string]any)
			if readPayload["op"] != "read" {
				t.Errorf("expected immutable verification read: %v", read)
			}
			_ = conn.WriteJSON(wireFrame("receipt", read["ref"].(string), map[string]any{
				"status": "ok", "value": json.RawMessage(`{"safe":true}`)}))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := LoginAndConnect(context.Background(), AtollConfig{BaseURL: server.URL,
		Email: "operator@example.test", Password: "local-password", ControlActorID: "tool:recruiting",
		Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	terminal, sender, err := client.Request(context.Background(), "recruiting.source.get", map[string]any{"id": "source-1"})
	if err != nil || sender != "human:operator:7" {
		t.Fatalf("request terminal=%v sender=%q err=%v", terminal, sender, err)
	}
	if err := client.CreateResource(context.Background(), "artifact://capture/evidence", json.RawMessage(`{"safe":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := client.EnsureResource(context.Background(), "artifact://capture/evidence", json.RawMessage(`{"safe":true}`)); err != nil {
		t.Fatalf("exact immutable Resource retry: %v", err)
	}
}

func TestAtollClientRejectsCredentialBearingOrRemotePlaintextURL(t *testing.T) {
	for _, value := range []string{"http://example.com", "https://user:password@example.com", "https://example.com/?token=secret", "https://example.com/base"} {
		if _, err := validateAtollURL(value); err == nil {
			t.Fatalf("unsafe Atoll URL accepted: %s", value)
		}
	}
	for _, value := range []string{"http://127.0.0.1:8080", "http://localhost:8080", "https://atoll.example.test"} {
		parsed, err := validateAtollURL(value)
		if err != nil || !strings.HasPrefix(parsed.String(), strings.Split(value, "/base")[0]) {
			t.Fatalf("safe Atoll URL rejected: %s err=%v", value, err)
		}
	}
}
