// Command narrative-web serves the evolving novel as a LAN-visible website.
package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	_ "modernc.org/sqlite"
)

func main() {
	fs := flag.NewFlagSet("narrative-web", flag.ExitOnError)
	addr := fs.String("addr", "0.0.0.0:8841", "listen address")
	content := fs.String("content", "narrative", "narrative content directory")
	atollHome := fs.String("atoll-home", "", "Atoll server home whose c0 ledger is the live source")
	_ = fs.Parse(os.Args[1:])

	handler, err := siteHandler(*content, *atollHome)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("《名册之外》已启动：http://%s", *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func siteHandler(content, atollHome string) (http.Handler, error) {
	index := filepath.Join(content, "site", "index.html")
	if info, err := os.Stat(index); err != nil || info.IsDir() {
		return nil, fmt.Errorf("site index not found at %s", index)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/world", yamlJSONHandler(filepath.Join(content, "world.yaml")))
	mux.HandleFunc("/api/cast", yamlJSONHandler(filepath.Join(content, "cast.yaml")))
	mux.HandleFunc("/api/constitution", yamlJSONHandler(filepath.Join(content, "constitution.yaml")))
	mux.HandleFunc("/api/public-runs", publicRunsHandler(content, atollHome))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","projection":"atoll-public-ledger"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, index)
			return
		}
		http.NotFound(w, r)
	})
	return mux, nil
}

type RunEntry struct {
	Day          int    `json:"day"`
	ChapterTitle string `json:"chapter_title"`
	POV          string `json:"pov"`
	Directory    string `json:"directory"`
}

type publicRun struct {
	RunEntry
	Source        string           `json:"source"`
	Chapter       string           `json:"chapter"`
	ChapterStatus string           `json:"chapter_status"`
	Events        []map[string]any `json:"events"`
	Decisions     []map[string]any `json:"decisions,omitempty"`
}

type publicProjection struct {
	Authority     string         `json:"authority"`
	DurableDay    int            `json:"durable_day"`
	FactCount     int            `json:"fact_count"`
	DecisionCount int            `json:"decision_count"`
	AutoRun       map[string]any `json:"auto_run,omitempty"`
	Runs          []publicRun    `json:"runs"`
}

type liveProjection struct {
	Facts     map[int][]map[string]any
	Decisions map[int][]map[string]any
	Chapters  map[int]map[string]any
	World     map[string]any
}

func publicRunsHandler(content, atollHome string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		projection, err := buildPublicProjection(content, atollHome)
		if err != nil {
			http.Error(w, "public projection unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(projection)
	}
}

func buildPublicProjection(content, atollHome string) (publicProjection, error) {
	var index []RunEntry
	if err := readJSON(filepath.Join(content, "runs", "index.json"), &index); err != nil {
		return publicProjection{}, err
	}
	live, err := readAtollProjection(atollHome)
	if err != nil {
		return publicProjection{}, err
	}
	durableDay := int(number(live.World["day"]))
	if durableDay == 0 {
		for day := range live.Facts {
			if day > durableDay {
				durableDay = day
			}
		}
	}
	projection := publicProjection{Authority: "atoll-c0", DurableDay: durableDay}
	if auto, ok := live.World["auto_run"].(map[string]any); ok {
		projection.AutoRun = auto
	}
	seenDays := map[int]bool{}
	for _, entry := range index {
		chapter, err := os.ReadFile(filepath.Join(content, "runs", entry.Directory, "chapter.md"))
		if err != nil {
			return publicProjection{}, err
		}
		events := live.Facts[entry.Day]
		source := "atoll"
		if entry.Day <= 2 {
			events, err = readJSONLines(filepath.Join(content, "runs", entry.Directory, "ledger.jsonl"))
			if err != nil {
				return publicProjection{}, err
			}
			source = "author-calibration"
		} else if entry.Day > durableDay {
			continue
		}
		seenDays[entry.Day] = true
		chapterText, chapterStatus := string(chapter), "published"
		if projected := live.Chapters[entry.Day]; source == "atoll" && chapterPublished(projected) {
			chapterText = fmt.Sprint(projected["markdown"])
			entry.ChapterTitle = fmt.Sprint(projected["title"])
			entry.POV = fmt.Sprint(projected["pov"])
		}
		projection.Runs = append(projection.Runs, publicRun{RunEntry: entry, Source: source, Chapter: chapterText, ChapterStatus: chapterStatus, Events: events, Decisions: live.Decisions[entry.Day]})
	}
	liveDaySet := map[int]bool{}
	for day, events := range live.Facts {
		projection.FactCount += len(events)
		if !seenDays[day] {
			liveDaySet[day] = true
		}
	}
	for day := range live.Chapters {
		if !seenDays[day] {
			liveDaySet[day] = true
		}
	}
	for day := range live.Decisions {
		if !seenDays[day] {
			liveDaySet[day] = true
		}
	}
	for _, rows := range live.Decisions {
		projection.DecisionCount += len(rows)
	}
	liveDays := make([]int, 0, len(liveDaySet))
	for day := range liveDaySet {
		liveDays = append(liveDays, day)
	}
	sort.Ints(liveDays)
	for _, day := range liveDays {
		title := fmt.Sprintf("第%d日事实纪要", day)
		pov, chapterText, chapterStatus := "尚未封章", "", "accumulating"
		if projected := live.Chapters[day]; chapterPublished(projected) {
			title, pov, chapterText = fmt.Sprint(projected["title"]), fmt.Sprint(projected["pov"]), fmt.Sprint(projected["markdown"])
			chapterStatus = "published"
		}
		projection.Runs = append(projection.Runs, publicRun{
			RunEntry: RunEntry{Day: day, ChapterTitle: title, POV: pov},
			Source:   "atoll", Chapter: chapterText, ChapterStatus: chapterStatus, Events: live.Facts[day], Decisions: live.Decisions[day],
		})
	}
	sort.SliceStable(projection.Runs, func(i, j int) bool { return projection.Runs[i].Day < projection.Runs[j].Day })
	return projection, nil
}

func chapterPublished(projected map[string]any) bool {
	status := projected["publication_status"]
	if status == nil {
		status = projected["status"]
	}
	return projected != nil && status == "published" && strings.TrimSpace(fmt.Sprint(projected["markdown"])) != ""
}

func factChapter(day int, title string, events []map[string]any) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# 第%d章 %s\n\n", day, title)
	for _, event := range events {
		fmt.Fprintf(&out, "%s %s\n\n", event["summary"], event["actual_effect"])
	}
	return strings.TrimSpace(out.String())
}

func readAtollProjection(home string) (liveProjection, error) {
	if strings.TrimSpace(home) == "" {
		return liveProjection{}, fmt.Errorf("atoll home is required")
	}
	dbPath := filepath.Join(home, "channels", "YzA.db")
	u := &url.URL{Scheme: "file", Path: dbPath}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return liveProjection{}, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT type,payload FROM messages WHERE type IN ('narrative.fact','narrative.decision','narrative.chapter') AND kind='event' ORDER BY seq`)
	if err != nil {
		return liveProjection{}, err
	}
	defer rows.Close()
	live := liveProjection{Facts: map[int][]map[string]any{}, Decisions: map[int][]map[string]any{}, Chapters: map[int]map[string]any{}, World: map[string]any{}}
	for rows.Next() {
		var eventType string
		var raw []byte
		if err := rows.Scan(&eventType, &raw); err != nil {
			return liveProjection{}, err
		}
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			return liveProjection{}, err
		}
		dayNumber, ok := event["day"].(float64)
		if !ok || dayNumber < 1 {
			return liveProjection{}, fmt.Errorf("Atoll public event has invalid day: %v", event["day"])
		}
		day := int(dayNumber)
		switch eventType {
		case "narrative.chapter":
			live.Chapters[day] = event
		case "narrative.decision":
			live.Decisions[day] = append(live.Decisions[day], event)
		case "narrative.fact":
			event["occurred_at"] = tickLabel(day, fmt.Sprint(event["tick"]))
			prior := live.Facts[day]
			if _, hasCausalProvenance := event["cause_event_ids"]; !hasCausalProvenance {
				if len(prior) > 0 {
					event["cause_event_ids"] = []string{fmt.Sprint(prior[len(prior)-1]["event_id"])}
				} else {
					event["cause_event_ids"] = []string{}
				}
			}
			live.Facts[day] = append(prior, event)
		}
	}
	if err := rows.Err(); err != nil {
		return liveProjection{}, err
	}
	var worldRaw []byte
	if err := db.QueryRow(`SELECT bytes FROM actor_state WHERE resource_id='narrative.world' ORDER BY created_at DESC LIMIT 1`).Scan(&worldRaw); err == nil {
		_ = json.Unmarshal(worldRaw, &live.World)
	}
	return live, nil
}

func number(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func tickLabel(day int, tick string) string {
	labels := map[string]string{"dawn": "卯正", "morning": "辰初", "late_morning": "巳初", "noon": "午初", "afternoon": "未初", "departure": "申初"}
	return fmt.Sprintf("同治六年五月 · 第%d日 %s", day, labels[tick])
}

func readJSON(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func readJSONLines(path string) ([]map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var rows []map[string]any
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return fmt.Sprint(rows[i]["event_id"]) < fmt.Sprint(rows[j]["event_id"]) })
	return rows, scanner.Err()
}

func yamlJSONHandler(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "source unavailable", http.StatusInternalServerError)
			return
		}
		var document any
		if err := yaml.Unmarshal(data, &document); err != nil {
			http.Error(w, "source invalid", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if err := json.NewEncoder(w).Encode(document); err != nil {
			http.Error(w, "encode failed", http.StatusInternalServerError)
		}
	}
}
