package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const wireVersion = 5

type AtollConfig struct {
	BaseURL        string
	Email          string
	Password       string
	ChannelID      string
	ControlActorID string
	Timeout        time.Duration
}

type AtollClient struct {
	baseURL        *url.URL
	httpClient     *http.Client
	conn           *websocket.Conn
	channelID      string
	controlActorID string
	timeout        time.Duration
	mu             sync.Mutex
	sequence       uint64
}

type WireError struct {
	Frame  string `json:"frame"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func (e *WireError) Error() string {
	if e.Detail == "" {
		return "Atoll " + e.Frame + ": " + e.Code
	}
	return "Atoll " + e.Frame + ": " + e.Code + ": " + e.Detail
}

type TerminalError struct {
	Code   string
	Detail string
}

func (e *TerminalError) Error() string {
	if e.Detail == "" {
		return "Atoll command failed: " + e.Code
	}
	return "Atoll command failed: " + e.Code + ": " + e.Detail
}

func LoginAndConnect(ctx context.Context, config AtollConfig) (*AtollClient, error) {
	base, err := validateAtollURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Email) == "" || config.Password == "" || strings.TrimSpace(config.ControlActorID) == "" {
		return nil, fmt.Errorf("Atoll email, password, and recruiting control actor ID are required")
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar, Timeout: config.Timeout}
	loginBody, _ := json.Marshal(map[string]string{"email": strings.TrimSpace(config.Email), "password": config.Password})
	loginURL := base.ResolveReference(&url.URL{Path: "/api/identity/login"})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, loginURL.String(), bytes.NewReader(loginBody))
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("login to Atoll: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("login to Atoll returned HTTP %d", response.StatusCode)
	}
	var identity map[string]any
	if err := json.NewDecoder(response.Body).Decode(&identity); err != nil {
		return nil, fmt.Errorf("decode Atoll login: %w", err)
	}
	channelID := strings.TrimSpace(config.ChannelID)
	if channelID == "" {
		channelID, _ = identity["home_channel_id"].(string)
	}
	if channelID == "" {
		return nil, fmt.Errorf("Atoll channel ID is required and login returned no home channel")
	}
	wsURL := *base
	if wsURL.Scheme == "https" {
		wsURL.Scheme = "wss"
	} else {
		wsURL.Scheme = "ws"
	}
	wsURL.Path, wsURL.RawQuery, wsURL.Fragment = "/ws", "", ""
	headers := http.Header{}
	headers.Set("Origin", base.Scheme+"://"+base.Host)
	for _, cookie := range jar.Cookies(base) {
		headers.Add("Cookie", cookie.String())
	}
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, wsURL.String(), headers)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		return nil, fmt.Errorf("connect to Atoll WebSocket (HTTP %d): %w", status, err)
	}
	client := &AtollClient{baseURL: base, httpClient: httpClient, conn: conn, channelID: channelID,
		controlActorID: strings.TrimSpace(config.ControlActorID), timeout: config.Timeout}
	if err := client.attach(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

func (c *AtollClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *AtollClient) Request(ctx context.Context, word string, payload any) (map[string]any, string, error) {
	if c == nil || c.conn == nil {
		return nil, "", errors.New("Atoll client is not connected")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := c.nextRef("submit")
	frame := wireFrame("submit", ref, map[string]any{"channel_id": c.channelID, "msg_type": word,
		"kind": "request", "visibility": "public", "payload": payload, "audience": []string{c.controlActorID}})
	if err := c.write(ctx, frame); err != nil {
		return nil, "", err
	}
	var messageID, sender string
	var terminal map[string]any
	type observedEnvelope struct {
		ID       string `json:"id"`
		ParentID string `json:"parent_id"`
		Kind     string `json:"kind"`
		Sender   struct {
			ID string `json:"id"`
		} `json:"sender"`
		Payload json.RawMessage `json:"payload"`
	}
	observed := make([]observedEnvelope, 0, 4)
	resolveObserved := func() {
		if messageID == "" {
			return
		}
		for _, item := range observed {
			if item.ID == messageID && item.Kind == "request" {
				sender = item.Sender.ID
			}
			if item.ParentID == messageID && item.Kind == "response" {
				var body map[string]any
				if json.Unmarshal(item.Payload, &body) == nil && (body["status"] == "completed" || body["status"] == "failed") {
					terminal = body
				}
			}
		}
	}
	for messageID == "" || sender == "" || terminal == nil {
		frame, err := c.read(ctx)
		if err != nil {
			return nil, "", err
		}
		if frame.Type == "error" && frame.Ref == ref {
			return nil, "", decodeWireError(frame)
		}
		if frame.Type == "receipt" && frame.Ref == ref {
			var receipt struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(frame.Payload, &receipt); err != nil || receipt.MessageID == "" {
				return nil, "", fmt.Errorf("Atoll submit receipt omitted message_id")
			}
			messageID = receipt.MessageID
			resolveObserved()
			continue
		}
		if frame.Type != "feed" {
			continue
		}
		var feed struct {
			Envelope json.RawMessage `json:"envelope"`
		}
		if json.Unmarshal(frame.Payload, &feed) != nil {
			continue
		}
		var envelope observedEnvelope
		if json.Unmarshal(feed.Envelope, &envelope) != nil {
			continue
		}
		if len(observed) >= 512 {
			return nil, "", fmt.Errorf("Atoll feed exceeded the bounded request correlation window")
		}
		observed = append(observed, envelope)
		resolveObserved()
	}
	if terminal["status"] != "completed" {
		code, _ := terminal["error_code"].(string)
		detail, _ := terminal["detail"].(string)
		return terminal, sender, &TerminalError{Code: code, Detail: detail}
	}
	return terminal, sender, nil
}

func (c *AtollClient) CreateResource(ctx context.Context, resourceID string, body json.RawMessage) error {
	if len(body) == 0 || strings.TrimSpace(resourceID) == "" {
		return fmt.Errorf("Resource ID and body are required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := c.nextRef("resource")
	if err := c.write(ctx, wireFrame("resource", ref, map[string]any{"channel_id": c.channelID,
		"op": "create", "resource_id": resourceID, "args": body})); err != nil {
		return err
	}
	for {
		frame, err := c.read(ctx)
		if err != nil {
			return err
		}
		if frame.Ref != ref {
			continue
		}
		if frame.Type == "error" {
			return decodeWireError(frame)
		}
		if frame.Type == "receipt" {
			var outcome struct{ Status, Detail string }
			if err := json.Unmarshal(frame.Payload, &outcome); err != nil {
				return fmt.Errorf("decode Resource receipt: %w", err)
			}
			if outcome.Status != "ok" {
				return fmt.Errorf("create Resource %s: %s: %s", resourceID, outcome.Status, outcome.Detail)
			}
			return nil
		}
	}
}

// EnsureResource makes a deterministic capture upload retryable. It never
// overwrites an existing Resource: after a create error it accepts only an
// exact byte-for-byte existing value at the same ID.
func (c *AtollClient) EnsureResource(ctx context.Context, resourceID string, body json.RawMessage) error {
	createErr := c.CreateResource(ctx, resourceID, body)
	if createErr == nil {
		return nil
	}
	existing, found, readErr := c.ReadResource(ctx, resourceID)
	if readErr == nil && found && bytes.Equal(existing, body) {
		return nil
	}
	if readErr != nil {
		return fmt.Errorf("create Resource failed (%v) and verification failed: %w", createErr, readErr)
	}
	if found {
		return fmt.Errorf("create Resource failed and existing immutable content differs: %w", createErr)
	}
	return createErr
}

func (c *AtollClient) ReadResource(ctx context.Context, resourceID string) (json.RawMessage, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := c.nextRef("resource-read")
	if err := c.write(ctx, wireFrame("resource", ref, map[string]any{"channel_id": c.channelID,
		"op": "read", "resource_id": resourceID})); err != nil {
		return nil, false, err
	}
	for {
		frame, err := c.read(ctx)
		if err != nil {
			return nil, false, err
		}
		if frame.Ref != ref {
			continue
		}
		if frame.Type == "error" {
			wireErr := decodeWireError(frame)
			var typed *WireError
			if errors.As(wireErr, &typed) && typed.Code == "resource_not_found" {
				return nil, false, nil
			}
			return nil, false, wireErr
		}
		if frame.Type == "receipt" {
			var outcome struct {
				Status string          `json:"status"`
				Detail string          `json:"detail"`
				Value  json.RawMessage `json:"value"`
			}
			if err := json.Unmarshal(frame.Payload, &outcome); err != nil {
				return nil, false, fmt.Errorf("decode Resource read receipt: %w", err)
			}
			if outcome.Status == "resource_not_found" {
				return nil, false, nil
			}
			if outcome.Status != "ok" {
				return nil, false, fmt.Errorf("read Resource %s: %s: %s", resourceID, outcome.Status, outcome.Detail)
			}
			return append(json.RawMessage(nil), outcome.Value...), true, nil
		}
	}
}

type wireEnvelope struct {
	V       int             `json:"v"`
	Type    string          `json:"frame_type"`
	Ref     string          `json:"ref,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (c *AtollClient) attach() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := c.nextRef("attach")
	if err := c.write(context.Background(), wireFrame("attach", ref, map[string]any{
		"since": map[string]int64{c.channelID: 0}, "focus": "", "history_protocol": wireVersion, "generation": 1})); err != nil {
		return err
	}
	for {
		frame, err := c.read(context.Background())
		if err != nil {
			return err
		}
		if frame.Ref != ref {
			continue
		}
		if frame.Type == "error" {
			return decodeWireError(frame)
		}
		if frame.Type == "receipt" {
			return nil
		}
	}
}

func (c *AtollClient) nextRef(prefix string) string {
	c.sequence++
	return fmt.Sprintf("recruiting-extension-%s-%d", prefix, c.sequence)
}

func (c *AtollClient) write(ctx context.Context, value any) error {
	deadline := time.Now().Add(c.timeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = c.conn.SetWriteDeadline(deadline)
	return c.conn.WriteJSON(value)
}

func (c *AtollClient) read(ctx context.Context) (wireEnvelope, error) {
	deadline := time.Now().Add(c.timeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = c.conn.SetReadDeadline(deadline)
	var frame wireEnvelope
	if err := c.conn.ReadJSON(&frame); err != nil {
		return wireEnvelope{}, err
	}
	return frame, nil
}

func wireFrame(kind, ref string, payload any) map[string]any {
	return map[string]any{"v": wireVersion, "frame_type": kind, "ref": ref, "payload": payload}
}

func decodeWireError(frame wireEnvelope) error {
	var wireErr WireError
	if err := json.Unmarshal(frame.Payload, &wireErr); err != nil {
		return fmt.Errorf("Atoll %s frame failed", frame.Ref)
	}
	return &wireErr
}

func validateAtollURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost"))) {
		return nil, fmt.Errorf("Atoll URL must be HTTPS or loopback HTTP without credentials, query, or fragment")
	}
	parsed.Path = ""
	return parsed, nil
}
