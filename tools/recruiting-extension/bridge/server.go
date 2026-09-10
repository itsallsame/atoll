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
)

const BridgeProtocolVersion = "recruiting.extension-bridge.v1"

type Service struct {
	Client atollSubmissionClient
	Token  string
	Now    func() time.Time
	mu     sync.Mutex
}

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
	if s == nil || s.Client == nil || len(s.Token) < 32 || s.Now == nil {
		return nil, fmt.Errorf("bridge service requires Atoll client, strong pairing token, and clock")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ready"}`))
	})
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
		conn.SetReadLimit(extensionDraftWireLimit)
		for {
			_, raw, readErr := conn.ReadMessage()
			if readErr != nil {
				return
			}
			response := s.handle(request.Context(), raw)
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if writeErr := conn.WriteJSON(response); writeErr != nil {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := SubmitDraft(ctx, s.Client, draft, s.Now)
	if err != nil {
		return bridgeResponse{Version: BridgeProtocolVersion, RequestID: request.RequestID,
			Status: "failed", Error: err.Error()}
	}
	return bridgeResponse{Version: BridgeProtocolVersion, RequestID: request.RequestID,
		Status: "completed", Result: &result}
}

func loopbackRemote(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}
