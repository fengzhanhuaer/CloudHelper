package core

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudhelper/probe_controller/internal/dashboard"
)

const (
	listenAddr         = "127.0.0.1:15030"
	dataDir            = "./data"
	mainStoreFile      = "cloudhelper.json"
	blacklistStoreFile = "blacklist.json"
	initialKeyLogFile  = "initial_key.log"

	nonceTTL          = 30 * time.Second
	sessionTTL        = 1 * time.Hour
	nonceRequestLimit = 5
)

// DataStore represents our JSON storage.
type DataStore struct {
	mu   sync.RWMutex
	path string
	Data map[string]interface{} `json:"data"`
}

var (
	Store           *DataStore
	serverStartTime time.Time
)

func initStore() {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("failed to create data directory: %v", err)
	}

	dbPath := filepath.Join(dataDir, mainStoreFile)
	Store = &DataStore{
		path: dbPath,
		Data: make(map[string]interface{}),
	}

	if _, err := os.Stat(dbPath); err == nil {
		content, readErr := os.ReadFile(dbPath)
		if readErr != nil {
			log.Fatalf("failed to read JSON data file: %v", readErr)
		}
		if len(content) > 0 {
			if unmarshalErr := json.Unmarshal(content, &Store.Data); unmarshalErr != nil {
				log.Fatalf("failed to parse JSON data file: %v", unmarshalErr)
			}
		}
	} else if os.IsNotExist(err) {
		if saveErr := Store.Save(); saveErr != nil {
			log.Fatalf("failed to initialize JSON data file: %v", saveErr)
		}
	} else {
		log.Fatalf("failed to check JSON data file: %v", err)
	}

	log.Println("JSON datastore initialized at", dbPath)
}

func (s *DataStore) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	content, err := json.MarshalIndent(s.Data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, content, 0o644)
}

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, CF-Connecting-IP, X-Forwarded-For, X-Real-IP")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func authRequiredMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := extractBearerToken(r)
		if err != nil || !IsTokenValid(token) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "invalid or expired session token",
			})
			return
		}
		next(w, r)
	}
}

func requireHTTPSMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isHTTPSRequest(r) {
			writeJSON(w, http.StatusUpgradeRequired, map[string]string{
				"error": "https is required",
			})
			return
		}
		next(w, r)
	}
}

func isHTTPSRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}

	xfp := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if xfp != "" {
		parts := strings.Split(xfp, ",")
		if len(parts) > 0 && strings.EqualFold(strings.TrimSpace(parts[0]), "https") {
			return true
		}
	}

	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Ssl")), "on") {
		return true
	}

	forwarded := strings.ToLower(strings.TrimSpace(r.Header.Get("Forwarded")))
	return strings.Contains(forwarded, "proto=https")
}

func PingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, statusPayload())
}

func dashboardStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard/status" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, statusPayload())
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard" {
		http.NotFound(w, r)
		return
	}
	dashboard.Handler(w, r)
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func statusPayload() map[string]interface{} {
	return map[string]interface{}{
		"message": "pong",
		"service": "CloudHelper Probe Controller",
		"uptime":  int(time.Since(serverStartTime).Seconds()),
	}
}

func Run() {
	serverStartTime = time.Now()

	initStore()
	initAuth()

	mux := NewMux()

	log.Println("CloudHelper Probe Controller is running at http://" + listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatal(err)
	}
}

func NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ping", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(PingHandler))))
	mux.HandleFunc("/api/auth/nonce", corsMiddleware(requireHTTPSMiddleware(NonceHandler)))
	mux.HandleFunc("/api/auth/login", corsMiddleware(requireHTTPSMiddleware(LoginHandler)))
	mux.HandleFunc("/api/admin/status", corsMiddleware(requireHTTPSMiddleware(authRequiredMiddleware(AdminStatusHandler))))
	mux.HandleFunc("/dashboard/status", dashboardStatusHandler)
	mux.HandleFunc("/dashboard", dashboardHandler)
	mux.HandleFunc("/", rootHandler)
	return mux
}

func SetServerStartTimeForTest(ts time.Time) {
	serverStartTime = ts
}
