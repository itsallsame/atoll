package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	payloadBytes := positiveEnv("RECRUITING_ORIGIN_PAYLOAD_BYTES", 4096)
	latency := time.Duration(positiveEnv("RECRUITING_ORIGIN_LATENCY_MS", 5)) * time.Millisecond
	listingLatency := latency
	if raw := strings.TrimSpace(os.Getenv("RECRUITING_ORIGIN_LISTING_LATENCY_MS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			log.Fatal("RECRUITING_ORIGIN_LISTING_LATENCY_MS must be a positive integer")
		}
		listingLatency = time.Duration(value) * time.Millisecond
	}
	listingItems := positiveEnv("RECRUITING_ORIGIN_LISTING_ITEMS", 1)
	activityUnix := int64(positiveEnv("RECRUITING_ORIGIN_ACTIVITY_UNIX", int(time.Now().UTC().Unix())))
	failureMatrix := strings.TrimSpace(os.Getenv("RECRUITING_ORIGIN_FAILURE_MATRIX")) == "1"
	var jobRequests atomic.Uint64
	var listingRequests atomic.Uint64
	var listedItems atomic.Uint64
	var robotsRequests atomic.Uint64
	var unavailableAttempts atomic.Uint64
	var throttledAttempts atomic.Uint64
	var unavailableRequests atomic.Uint64
	var throttledRequests atomic.Uint64
	var forbiddenRequests atomic.Uint64
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/robots.txt", func(response http.ResponseWriter, _ *http.Request) {
		robotsRequests.Add(1)
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = response.Write([]byte("User-agent: *\nAllow: /\n"))
	})
	mux.HandleFunc("/metrics", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]uint64{
			"job_requests": jobRequests.Load(), "listing_requests": listingRequests.Load(),
			"listing_items": listedItems.Load(), "robots_requests": robotsRequests.Load(),
			"injected_503": unavailableRequests.Load(), "injected_429": throttledRequests.Load(),
			"injected_403": forbiddenRequests.Load(),
		})
	})
	mux.HandleFunc("/listing", func(response http.ResponseWriter, request *http.Request) {
		offset, err := nonNegativeQuery(request, "offset", 0)
		if err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		limit, err := positiveQuery(request, "limit", 500)
		if err != nil || limit > 500 {
			http.Error(response, "limit must be in [1,500]", http.StatusBadRequest)
			return
		}
		listingRequests.Add(1)
		time.Sleep(listingLatency)
		body, emitted := listingResponse(request.Host, listingItems, activityUnix, offset, limit)
		listedItems.Add(uint64(emitted))
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = response.Write(body)
	})
	mux.HandleFunc("/jobs/", func(response http.ResponseWriter, request *http.Request) {
		id := strings.TrimPrefix(request.URL.Path, "/jobs/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(response, request)
			return
		}
		jobRequests.Add(1)
		time.Sleep(latency)
		if failureMatrix {
			switch id {
			case "000000":
				if unavailableAttempts.Add(1) == 1 {
					unavailableRequests.Add(1)
					writeInjectedFailure(response, http.StatusServiceUnavailable, "temporary upstream outage")
					return
				}
			case "000001":
				if throttledAttempts.Add(1) == 1 {
					throttledRequests.Add(1)
					response.Header().Set("Retry-After", "1")
					writeInjectedFailure(response, http.StatusTooManyRequests, "temporary origin throttle")
					return
				}
			case "000002":
				forbiddenRequests.Add(1)
				writeInjectedFailure(response, http.StatusForbidden, "persistent access denial")
				return
			}
		}
		body := responseBody(id, payloadBytes)
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = response.Write(body)
	})
	mux.HandleFunc("/browser-jobs/", func(response http.ResponseWriter, request *http.Request) {
		id := strings.TrimPrefix(request.URL.Path, "/browser-jobs/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(response, request)
			return
		}
		jobRequests.Add(1)
		time.Sleep(latency)
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = response.Write(browserResponseBody(id, payloadBytes))
	})
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("recruiting controlled origin listening on %s payload_bytes=%d latency_ms=%d listing_latency_ms=%d listing_items=%d",
		server.Addr, payloadBytes, latency.Milliseconds(), listingLatency.Milliseconds(), listingItems)
	log.Fatal(server.ListenAndServe())
}

func writeInjectedFailure(response http.ResponseWriter, status int, detail string) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_, _ = response.Write([]byte(detail))
}

func positiveQuery(request *http.Request, name string, fallback int) (int, error) {
	value := strings.TrimSpace(request.URL.Query().Get(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return parsed, nil
}

func nonNegativeQuery(request *http.Request, name string, fallback int) (int, error) {
	value := strings.TrimSpace(request.URL.Query().Get(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s must be non-negative", name)
	}
	return parsed, nil
}

func listingResponse(host string, itemCount int, activityUnix int64, offset, limit int) ([]byte, int) {
	total := itemCount
	end := offset + limit
	if end > total {
		end = total
	}
	if offset > total {
		offset = total
	}
	items := make([]map[string]any, 0, end-offset)
	for index := offset; index < end; index++ {
		id := fmt.Sprintf("%06d", index)
		activity := time.Unix(activityUnix-int64(index), 0).UTC()
		if index == itemCount-1 {
			// The final row is older than the seeded checkpoint. It proves the
			// activity boundary and lets the incremental scan close safely.
			activity = time.Unix(activityUnix-172800, 0).UTC()
		}
		items = append(items, map[string]any{
			"id": id, "title": "Daily Capacity Role " + id,
			"url": "http://" + host + "/jobs/" + id, "activity_at": activity.Format(time.RFC3339),
		})
	}
	body, _ := json.Marshal(map[string]any{
		"offset": offset, "limit": limit, "totalFound": total, "content": items,
	})
	return body, len(items)
}

func positiveEnv(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		log.Fatalf("%s must be a positive integer", name)
	}
	return parsed
}

func responseBody(id string, targetBytes int) []byte {
	prefix := fmt.Sprintf(`{"id":%q,"title":%q,"url":%q,"padding":"`, id,
		"HTTP Capacity Role "+id, "http://controlled-origin.invalid/jobs/"+id)
	suffix := `"}`
	padding := targetBytes - len(prefix) - len(suffix)
	if padding < 0 {
		padding = 0
	}
	return []byte(prefix + strings.Repeat("x", padding) + suffix)
}

func browserResponseBody(id string, targetBytes int) []byte {
	prefix := fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><title>Browser Capacity</title></head><body><main class="job"><span class="job-id" data-id="browser-job-%s"></span><h1 class="title">Browser Capacity Role %s</h1><a class="job-url" href="/browser-jobs/%s">Role</a><div class="padding">`, id, id, id)
	suffix := `</div></main></body></html>`
	padding := targetBytes - len(prefix) - len(suffix)
	if padding < 0 {
		padding = 0
	}
	return []byte(prefix + strings.Repeat("x", padding) + suffix)
}
