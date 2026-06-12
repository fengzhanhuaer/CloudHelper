package main

import (
	"net/http"
	"time"
)

func registerProbeOpenAIStyleCamouflageRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeProbeOpenAIStyleMethodNotAllowed(w, r.Method)
			return
		}
		sleepProbeOpenAIStyleJitter()
		writeJSON(w, http.StatusOK, map[string]any{
			"message":   "OpenAI-compatible API endpoint",
			"api_base":  "/v1",
			"version":   BuildVersion,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/v1", func(w http.ResponseWriter, r *http.Request) {
		sleepProbeOpenAIStyleJitter()
		writeProbeOpenAIStyleUnauthorized(w)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		sleepProbeOpenAIStyleJitter()
		writeProbeOpenAIStyleUnauthorized(w)
	})
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		sleepProbeOpenAIStyleJitter()
		writeProbeOpenAIStyleUnauthorized(w)
	})
}
