package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func newHTTPHandler(client *marketClient) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/listings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		force := r.URL.Query().Get("force") == "1"
		data, err := client.getListings(r.Context(), force)
		if err != nil {
			var rateLimit *rateLimitError
			if errors.As(err, &rateLimit) {
				retryAfter := int(rateLimit.retryAfter.Round(time.Second).Seconds())
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				status := http.StatusTooManyRequests
				if rateLimit.source == "app" {
					status = http.StatusServiceUnavailable
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": err.Error(), "retry_after_seconds": retryAfter, "source": rateLimit.source,
				})
				return
			}
			log.Printf("load market listings: %v", err)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "Не удалось загрузить данные Steam Market: " + err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(data); err != nil {
			log.Printf("encode listings response: %v", err)
		}
	})
	mux.Handle("/", frontendHandler("frontend/dist"))
	return mux
}

func frontendHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(root, filepath.Clean(r.URL.Path))
		if r.URL.Path != "/" {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		index := filepath.Join(root, "index.html")
		if _, err := os.Stat(index); err == nil {
			http.ServeFile(w, r, index)
			return
		}
		http.Error(w, "Сначала соберите интерфейс: cd frontend && npm install && npm run build", http.StatusServiceUnavailable)
	})
}
