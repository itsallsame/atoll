// Command narrative-web serves the evolving novel as a LAN-visible website.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/goccy/go-yaml"
)

func main() {
	fs := flag.NewFlagSet("narrative-web", flag.ExitOnError)
	addr := fs.String("addr", "0.0.0.0:8841", "listen address")
	content := fs.String("content", "narrative", "narrative content directory")
	_ = fs.Parse(os.Args[1:])

	handler, err := siteHandler(*content)
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

func siteHandler(content string) (http.Handler, error) {
	index := filepath.Join(content, "site", "index.html")
	if info, err := os.Stat(index); err != nil || info.IsDir() {
		return nil, fmt.Errorf("site index not found at %s", index)
	}
	files := http.FileServer(http.Dir(content))
	mux := http.NewServeMux()
	mux.HandleFunc("/api/world", yamlJSONHandler(filepath.Join(content, "world.yaml")))
	mux.HandleFunc("/api/cast", yamlJSONHandler(filepath.Join(content, "cast.yaml")))
	mux.HandleFunc("/api/constitution", yamlJSONHandler(filepath.Join(content, "constitution.yaml")))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, index)
			return
		}
		files.ServeHTTP(w, r)
	})
	return mux, nil
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
