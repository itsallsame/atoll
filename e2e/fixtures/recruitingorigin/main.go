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
	var jobRequests atomic.Uint64
	var robotsRequests atomic.Uint64
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
			"job_requests": jobRequests.Load(), "robots_requests": robotsRequests.Load(),
		})
	})
	mux.HandleFunc("/jobs/", func(response http.ResponseWriter, request *http.Request) {
		id := strings.TrimPrefix(request.URL.Path, "/jobs/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(response, request)
			return
		}
		jobRequests.Add(1)
		time.Sleep(latency)
		body := responseBody(id, payloadBytes)
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		_, _ = response.Write(body)
	})
	server := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("recruiting controlled origin listening on %s payload_bytes=%d latency_ms=%d",
		server.Addr, payloadBytes, latency.Milliseconds())
	log.Fatal(server.ListenAndServe())
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
