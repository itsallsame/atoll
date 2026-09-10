// Command narrative-atoll installs the evolving novel as native actors in an
// Atoll channel and advances it through the collaboration ledger.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/wanpengxie/atoll/platform/subjectgate"
	_ "modernc.org/sqlite"
)

const (
	worldClass             = "narrative-world"
	characterClass         = "narrative-character"
	environmentClass       = "narrative-environment"
	projectorClass         = "narrative-projector"
	plannerClass           = "narrative-planner"
	writerClass            = "narrative-writer"
	criticClass            = "narrative-critic"
	worldDeclaration       = "novel-evolution-world"
	worldMember            = "narrative-world"
	environmentDeclaration = "novel-evolution-environment"
	projectorDeclaration   = "novel-evolution-projector"
	plannerDeclaration     = "novel-evolution-planner"
	writerDeclaration      = "novel-evolution-writer"
	criticDeclaration      = "novel-evolution-critic"
)

type castFile struct {
	Characters []struct {
		ID string `yaml:"id"`
	} `yaml:"characters"`
}

type remoteError struct {
	Code   string
	Detail string
}

func (e *remoteError) Error() string { return e.Code + ": " + e.Detail }

func main() {
	addr := flag.String("addr", "http://127.0.0.1:8832", "Atoll HTTP origin")
	tokenFile := flag.String("token-file", defaultTokenFile(), "Atoll bearer token file")
	bundle := flag.String("bundle", "narrative", "narrative bundle visible to the Atoll server")
	channelName := flag.String("channel-name", "narrative", "prefix for each character's private child channel under c0")
	day := flag.Int("day", 0, "day to evolve; zero means the next durable day")
	through := flag.Int("through", 0, "advance repeatedly until this durable day")
	delay := flag.Duration("schedule", 0, "use an Atoll durable timer instead of evolving immediately")
	status := flag.Bool("status", false, "read durable world status without evolving")
	inject := flag.String("inject", "", "JSON file containing one audited external stimulus")
	autoThrough := flag.Int("auto-through", 0, "start bounded durable autorun through this day")
	interval := flag.Duration("interval", time.Minute, "interval between durable autorun ticks")
	maxEmptyDays := flag.Int("max-empty-days", 2, "stop autorun after this many consecutive days without new facts")
	stopAuto := flag.Bool("stop-auto", false, "stop durable autorun")
	reprojectFrom := flag.Int("reproject-from", 0, "rebuild public chapters from this ledger day through --through")
	backfillProvenanceFlag := flag.Bool("backfill-provenance", false, "derive missing state provenance from the c0 fact ledger")
	flag.Parse()

	absBundle, err := filepath.Abs(*bundle)
	if err != nil {
		log.Fatal(err)
	}
	characters, err := loadCharacters(absBundle)
	if err != nil {
		log.Fatal(err)
	}
	tokenRaw, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Fatalf("read token: %v", err)
	}
	token := strings.TrimSpace(string(tokenRaw))
	if token == "" {
		log.Fatal("token file is empty")
	}

	c0, err := dial(*addr, token, "c0")
	if err != nil {
		log.Fatal(err)
	}
	rootID, err := findRoot(c0)
	if err != nil {
		c0.Close()
		log.Fatal(err)
	}
	if err := ensureTemplates(c0, absBundle, *channelName, characters); err != nil {
		c0.Close()
		log.Fatal(err)
	}
	characterChannels, err := ensureTopology(c0, *channelName, rootID, characters)
	if err != nil {
		c0.Close()
		log.Fatal(err)
	}
	if err := waitForWorld(c0); err != nil {
		c0.Close()
		log.Fatal(err)
	}
	if err := waitForCharacters(c0, *channelName, characters); err != nil {
		c0.Close()
		log.Fatal(err)
	}
	client := c0
	defer client.Close()
	const channelID = "c0"

	var result map[string]any
	switch {
	case *inject != "":
		var payload map[string]any
		raw, readErr := os.ReadFile(*inject)
		if readErr != nil {
			err = readErr
			break
		}
		if decodeErr := json.Unmarshal(raw, &payload); decodeErr != nil {
			err = decodeErr
			break
		}
		result, err = client.Request(channelID, "narrative.inject", worldMember, payload)
	case *stopAuto:
		result, err = client.Request(channelID, "narrative.autorun.stop", worldMember, map[string]any{})
	case *autoThrough > 0:
		result, err = client.Request(channelID, "narrative.autorun.start", worldMember, map[string]any{
			"interval_ms": interval.Milliseconds(), "through": *autoThrough, "max_empty_days": *maxEmptyDays,
		})
	case *reprojectFrom > 0:
		result, err = reprojectLedger(client, filepath.Join(filepath.Dir(*tokenFile), "channels", "YzA.db"), *reprojectFrom, *through)
	case *backfillProvenanceFlag:
		result, err = backfillProvenance(client, filepath.Join(filepath.Dir(*tokenFile), "channels", "YzA.db"))
	case *status:
		result, err = client.Request(channelID, "narrative.status", worldMember, map[string]any{})
	case *through > 0:
		result, err = evolveThrough(client, channelID, *through)
	case *delay > 0:
		result, err = client.Request(channelID, "narrative.schedule", worldMember, map[string]any{"delay_ms": delay.Milliseconds(), "day": *day})
		if err == nil {
			fmt.Fprintf(os.Stderr, "scheduled on Atoll durable timer: %v\n", result["timer_id"])
			result, err = client.AwaitEvent("narrative.day.completed", *delay+2*time.Minute)
		}
	default:
		result, err = client.Request(channelID, "narrative.evolve", worldMember, map[string]any{"day": *day})
	}
	if err != nil {
		log.Fatal(err)
	}
	result["channel_id"] = channelID
	result["character_channels"] = characterChannels
	encoded, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(encoded))
}

func backfillProvenance(client *wsClient, dbPath string) (map[string]any, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT payload FROM messages WHERE kind='event' AND type='narrative.fact' ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := map[string]string{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event struct {
			EventID string         `json:"event_id"`
			Delta   map[string]any `json:"world_state_delta"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		for key := range event.Delta {
			sources[key] = event.EventID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return client.Request("c0", "narrative.provenance.backfill", worldMember, map[string]any{"sources": sources})
}

func reprojectLedger(client *wsClient, dbPath string, from, through int) (map[string]any, error) {
	if through < from {
		return nil, fmt.Errorf("--through must be at least --reproject-from")
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT payload FROM messages WHERE kind='event' AND type='narrative.fact' ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[int][]map[string]any{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		day := int(number(event["day"]))
		if day >= from && day <= through {
			byDay[day] = append(byDay[day], event)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := client.Request("c0", "narrative.project.reset", "narrative-projector", map[string]any{"from_day": from}); err != nil {
		return nil, fmt.Errorf("reset projector replay buffer: %w", err)
	}
	var result map[string]any
	for day := from; day <= through; day++ {
		events := byDay[day]
		result, err = client.Request("c0", "narrative.project", "narrative-projector", map[string]any{
			"day": day, "event_count": len(events), "events": events, "state": map[string]any{},
		})
		if err != nil {
			return nil, fmt.Errorf("reproject day %d: %w", day, err)
		}
	}
	return result, nil
}

func evolveThrough(client *wsClient, channelID string, through int) (map[string]any, error) {
	status, err := client.Request(channelID, "narrative.status", worldMember, map[string]any{})
	if err != nil {
		return nil, err
	}
	current := int(number(status["day"]))
	if through < current {
		return nil, fmt.Errorf("through day %d is behind durable day %d", through, current)
	}
	result := status
	for current < through {
		current++
		result, err = client.Request(channelID, "narrative.evolve", worldMember, map[string]any{"day": current})
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func number(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	default:
		return 0
	}
}

func defaultTokenFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".atoll/server/atoll-token"
	}
	return filepath.Join(home, ".atoll", "server", "atoll-token")
}

func loadCharacters(bundle string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(bundle, "cast.yaml"))
	if err != nil {
		return nil, err
	}
	var cast castFile
	if err := yaml.Unmarshal(raw, &cast); err != nil {
		return nil, err
	}
	characters := make([]string, 0, len(cast.Characters))
	for _, row := range cast.Characters {
		if row.ID != "" {
			characters = append(characters, row.ID)
		}
	}
	if len(characters) == 0 {
		return nil, errors.New("cast has no characters")
	}
	return characters, nil
}

func ensureTemplates(client *wsClient, bundle, channelPrefix string, characters []string) error {
	classes, err := client.Request("c0", "system.class.list", "system", map[string]any{})
	if err != nil {
		return err
	}
	if !containsClass(classes, worldClass) || !containsClass(classes, characterClass) || !containsClass(classes, environmentClass) || !containsClass(classes, projectorClass) || !containsClass(classes, plannerClass) || !containsClass(classes, writerClass) || !containsClass(classes, criticClass) {
		return fmt.Errorf("running Atoll node does not contain all narrative runtime classes; rebuild and restart it from this branch")
	}
	declarations := []map[string]any{{
		"id": worldDeclaration, "name": worldMember, "description": "《名册之外》世界状态、裁决与时间推进",
		"class": worldClass, "visibility": "private", "singleton": true,
		"config": map[string]any{"bundle": bundle, "start_day": 2, "channel_prefix": channelPrefix},
	}, {
		"id": environmentDeclaration, "name": "narrative-environment", "description": "《名册之外》环境与连续系统",
		"class": environmentClass, "visibility": "private", "singleton": true,
		"config": map[string]any{"bundle": bundle, "start_day": 2},
	}, {
		"id": projectorDeclaration, "name": "narrative-projector", "description": "《名册之外》公共事实叙事投影",
		"class": projectorClass, "visibility": "private", "singleton": true,
		"config": map[string]any{"bundle": bundle, "start_day": 2},
	}, {
		"id": plannerDeclaration, "name": "narrative-planner", "description": "《名册之外》因果场景与章节结构规划",
		"class": plannerClass, "visibility": "private", "singleton": true,
		"config": map[string]any{"bundle": bundle, "start_day": 2},
	}, {
		"id": writerDeclaration, "name": "narrative-writer", "description": "《名册之外》整章写作与有限返修",
		"class": writerClass, "visibility": "private", "singleton": true,
		"config": map[string]any{"bundle": bundle, "start_day": 2},
	}, {
		"id": criticDeclaration, "name": "narrative-critic", "description": "《名册之外》独立叙事与事实审稿",
		"class": criticClass, "visibility": "private", "singleton": true,
		"config": map[string]any{"bundle": bundle, "start_day": 2},
	}}
	for _, character := range characters {
		declarations = append(declarations, map[string]any{
			"id": declarationID(character), "name": memberName(character), "description": "《名册之外》人物：" + character,
			"class": characterClass, "visibility": "private", "singleton": true,
			"config": map[string]any{"bundle": bundle, "character": character, "start_day": 2},
		})
	}
	for _, declaration := range declarations {
		if _, err := client.Request("c0", "system.actor.template.create", "system", declaration); err != nil {
			if !isConflict(err) {
				return fmt.Errorf("create template %s: %w", declaration["id"], err)
			}
		}
	}
	return nil
}

func ensureTopology(client *wsClient, prefix, rootID string, characters []string) (map[string]string, error) {
	for _, declaration := range []string{worldDeclaration, environmentDeclaration, plannerDeclaration, writerDeclaration, criticDeclaration, projectorDeclaration} {
		if _, err := client.Request("c0", "system.member.create", "system", map[string]any{"decl_id": declaration}); err != nil {
			if !isConflict(err) {
				return nil, fmt.Errorf("seat %s in c0: %w", declaration, err)
			}
		}
	}
	channels := make(map[string]string, len(characters))
	for _, character := range characters {
		name := characterChannelName(prefix, character)
		reply, err := client.Request("c0", "system.channel.create", "system", map[string]any{
			"name": name, "initial_actor_ids": []string{rootID},
			"recipe": map[string]any{
				"declarations": []any{map[string]any{"decl_id": declarationID(character)}},
				"profile": map[string]any{
					"description": "《名册之外》人物私有认知空间：" + character,
					"serving":     1,
					"endpoints": map[string]any{
						"narrative.observe": map[string]any{"receiver": declarationID(character)},
						"narrative.propose": map[string]any{"receiver": declarationID(character)},
					},
				},
			},
		})
		if err != nil {
			if isConflict(err) {
				channels[character] = characterChannel(prefix, character)
				continue
			}
			return nil, fmt.Errorf("create private channel for %s: %w", character, err)
		}
		value := nestedValue(reply)
		channelID, _ := value["channel_id"].(string)
		if channelID == "" {
			return nil, fmt.Errorf("channel create for %s omitted channel_id: %v", character, reply)
		}
		channels[character] = channelID
	}
	return channels, nil
}

func isConflict(err error) bool {
	var remote *remoteError
	return errors.As(err, &remote) && remote.Code == "conflict_exists"
}

func findRoot(client *wsClient) (string, error) {
	reply, err := client.Request("c0", "system.member.list", "system", map[string]any{})
	if err != nil {
		return "", err
	}
	actors, _ := reply["actors"].([]any)
	for _, raw := range actors {
		row, _ := raw.(map[string]any)
		id, _ := row["id"].(string)
		if strings.HasPrefix(id, "human:root:") {
			return id, nil
		}
	}
	return "", errors.New("root actor is not seated in c0")
}

func waitForWorld(client *wsClient) error {
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		_, last = client.Request(client.focus, "narrative.status", worldMember, map[string]any{})
		if last == nil {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("narrative world did not become ready: %w", last)
}

func waitForCharacters(client *wsClient, prefix string, characters []string) error {
	// Channel creation and actor reconciliation are asynchronous. Give every
	// freshly minted channel one quiet interval before sending an application
	// request, so readiness probing does not itself become a stranded ledger row.
	time.Sleep(time.Second)
	for _, character := range characters {
		deadline := time.Now().Add(30 * time.Second)
		var last error
		for time.Now().Before(deadline) {
			_, last = client.Request("c0", "narrative.propose", peerName(prefix, character), map[string]any{"day": 1, "tick": "readiness"})
			if last == nil {
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
		if last != nil {
			return fmt.Errorf("character %s did not become reachable through its private channel: %w", character, last)
		}
	}
	return nil
}

func containsClass(reply map[string]any, wanted string) bool {
	collections := make([][]any, 0, 3)
	if rows, ok := reply["value"].([]any); ok {
		collections = append(collections, rows)
	}
	value := nestedValue(reply)
	for _, key := range []string{"classes", "items"} {
		if rows, ok := value[key].([]any); ok {
			collections = append(collections, rows)
		}
	}
	for _, rows := range collections {
		for _, raw := range rows {
			switch row := raw.(type) {
			case string:
				if row == wanted {
					return true
				}
			case map[string]any:
				if row["class"] == wanted || row["name"] == wanted {
					return true
				}
			}
		}
	}
	return false
}

func nestedValue(reply map[string]any) map[string]any {
	if value, ok := reply["value"].(map[string]any); ok {
		return value
	}
	return reply
}

func memberName(character string) string {
	return "narrative-" + strings.ReplaceAll(character, "_", "-")
}
func declarationID(character string) string {
	return "novel-character-" + strings.ReplaceAll(character, "_", "-")
}
func characterChannel(prefix, character string) string {
	return "c0." + characterChannelName(prefix, character)
}
func characterChannelName(prefix, character string) string {
	return prefix + "-" + strings.ReplaceAll(character, "_", "-")
}
func peerName(prefix, character string) string {
	return "peer:" + characterChannel(prefix, character)
}

type wsClient struct {
	conn  *websocket.Conn
	focus string
	acks  chan map[string]any
	feed  chan map[string]any
	done  chan struct{}
	once  sync.Once
}

func dial(origin, token, focus string) (*wsClient, error) {
	u, err := url.Parse(strings.TrimRight(origin, "/"))
	if err != nil {
		return nil, err
	}
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}
	wsURL := scheme + "://" + u.Host + "/ws"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		return nil, fmt.Errorf("dial %s: status=%d: %w", wsURL, status, err)
	}
	client := &wsClient{conn: conn, focus: focus, acks: make(chan map[string]any, 64), feed: make(chan map[string]any, 2048), done: make(chan struct{})}
	go client.readLoop()
	ack, err := client.send("attach", "attach", map[string]any{"since": map[string]int64{}, "focus": focus, "history_protocol": subjectgate.FrameVersion, "generation": 1})
	if err != nil {
		client.Close()
		return nil, err
	}
	if ack["frame_type"] != "receipt" {
		client.Close()
		return nil, fmt.Errorf("attach rejected: %v", ack)
	}
	return client, nil
}

func (c *wsClient) readLoop() {
	defer close(c.done)
	for {
		var frame map[string]any
		if err := c.conn.ReadJSON(&frame); err != nil {
			return
		}
		switch frame["frame_type"] {
		case "receipt", "error":
			c.acks <- frame
		case "feed":
			if payload, ok := frame["payload"].(map[string]any); ok {
				c.feed <- payload
			}
		}
	}
}

func (c *wsClient) Close() { c.once.Do(func() { _ = c.conn.Close() }) }

func (c *wsClient) send(ref, frameType string, payload any) (map[string]any, error) {
	frame := map[string]any{"v": subjectgate.FrameVersion, "frame_type": frameType, "ref": ref, "payload": payload}
	if err := c.conn.WriteJSON(frame); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case ack := <-c.acks:
			if ack["ref"] == ref {
				return ack, nil
			}
		case <-c.done:
			return nil, errors.New("websocket closed")
		case <-deadline.C:
			return nil, fmt.Errorf("no acknowledgement for %s", ref)
		}
	}
}

func (c *wsClient) Request(channelID, msgType, audience string, payload any) (map[string]any, error) {
	ref := uuid.NewString()
	ack, err := c.send(ref, "submit", map[string]any{
		"channel_id": channelID, "msg_type": msgType, "kind": "request", "visibility": "public",
		"audience": []string{audience}, "payload": payload,
	})
	if err != nil {
		return nil, err
	}
	if ack["frame_type"] == "error" {
		return nil, fmt.Errorf("submit %s rejected: %v", msgType, ack["payload"])
	}
	receipt, _ := ack["payload"].(map[string]any)
	requestID, _ := receipt["message_id"].(string)
	if requestID == "" {
		return nil, fmt.Errorf("submit %s omitted message_id", msgType)
	}
	deadline := time.NewTimer(40 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case item := <-c.feed:
			envelope, _ := item["envelope"].(map[string]any)
			if envelope == nil || envelope["kind"] != "response" || envelope["parent_id"] != requestID {
				continue
			}
			body, _ := envelope["payload"].(map[string]any)
			status, _ := body["status"].(string)
			if status == "completed" {
				return body, nil
			}
			if status == "failed" {
				return nil, &remoteError{Code: fmt.Sprint(body["error_code"]), Detail: fmt.Sprint(body["detail"])}
			}
		case <-c.done:
			return nil, errors.New("websocket closed while awaiting response")
		case <-deadline.C:
			return nil, fmt.Errorf("request %s timed out", msgType)
		}
	}
}

func (c *wsClient) AwaitEvent(msgType string, timeout time.Duration) (map[string]any, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case item := <-c.feed:
			envelope, _ := item["envelope"].(map[string]any)
			if envelope != nil && envelope["kind"] == "event" && envelope["type"] == msgType {
				payload, _ := envelope["payload"].(map[string]any)
				return payload, nil
			}
		case <-c.done:
			return nil, errors.New("websocket closed while awaiting event")
		case <-deadline.C:
			return nil, fmt.Errorf("event %s timed out", msgType)
		}
	}
}
