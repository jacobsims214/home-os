// Command calendar is the CalDAV calendar service for Home OS.
// It serves a CalDAV-compatible HTTP API on the configured port.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"home-os/calendar/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "calendar: %v\n", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler)

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("calendar: starting on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("calendar: server error: %v", err)
	}
}

// healthHandler responds with a JSON status payload indicating the service is
// healthy. CalDAV clients do not use this endpoint — it is for internal health
// checks (Kubernetes probes, Docker healthcheck, monitoring).
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"service": "home-os-calendar",
	})
}
