package main

import (
	"errors"
	"log"
	"net"
	"net/http"
	"os"
)

func main() {
	client := newMarketClient()
	if statePath := os.Getenv("STATE_FILE"); statePath != "" {
		client.statePath = statePath
	}
	if err := client.loadState(); err != nil {
		log.Fatalf("load market state: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	log.Printf("TB Market Parser listening on http://localhost:%s", port)
	if err := http.ListenAndServe(net.JoinHostPort(host, port), newHTTPHandler(client)); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
