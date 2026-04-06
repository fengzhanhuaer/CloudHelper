package backend

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var BuildVersion = "dev"

const (
	frontendWatchdogTimeout      = 45 * time.Second
	frontendWatchdogCheckInterval = 10 * time.Second
	frontendWatchdogStartupGrace = 90 * time.Second
)

var globalNetworkAssistantService *networkAssistantService

// App struct
type App struct {
	ctx              context.Context
	networkAssistant *networkAssistantService
	aiDebugService   *aiDebugService

	frontendWatchdogMu   sync.Mutex
	frontendWatchdogStop chan struct{}
	frontendHeartbeatAt  atomic.Int64
	frontendHeartbeatSeen atomic.Bool
	terminating          atomic.Bool
}

type PrivateKeyStatus struct {
	Found   bool   `json:"found"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{
		networkAssistant: newNetworkAssistantService(),
		aiDebugService:   newAIDebugService(),
	}
}

// Startup is called when the app starts.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	a.startFrontendWatchdog()
	if err := cleanupManagerStaleExecutables(); err != nil {
		logManagerWarnf("failed to cleanup stale manager executable files: %v", err)
	}
	if err := autoBackupManagerData(); err != nil {
		logManagerWarnf("failed to backup manager data: %v", err)
	}
	a.networkAssistant.UpdateSession("", "")
	if err := a.applyAIDebugListenFromConfig(); err != nil {
		logManagerWarnf("failed to apply AI debug listen config: %v", err)
	}
}

func (a *App) Shutdown(ctx context.Context) {
	a.terminating.Store(true)
	a.stopFrontendWatchdog()
	_, _ = stopProbeLinkSession("manager shutdown")
	if err := a.stopAIDebugServer(); err != nil {
		logManagerWarnf("failed to shutdown AI debug server: %v", err)
	}
	if a.networkAssistant == nil {
		return
	}
	if err := a.networkAssistant.Shutdown(); err != nil {
		logManagerWarnf("failed to shutdown network assistant: %v", err)
	}
}

func (a *App) NotifyFrontendHeartbeat() {
	if a == nil {
		return
	}
	a.frontendHeartbeatSeen.Store(true)
	a.frontendHeartbeatAt.Store(time.Now().Unix())
}

func (a *App) startFrontendWatchdog() {
	if a == nil {
		return
	}
	a.frontendWatchdogMu.Lock()
	if a.frontendWatchdogStop != nil {
		a.frontendWatchdogMu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	a.frontendWatchdogStop = stopCh
	a.frontendWatchdogMu.Unlock()

	startedAt := time.Now()
	go func() {
		ticker := time.NewTicker(frontendWatchdogCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				if a.terminating.Load() {
					return
				}
				if !a.frontendHeartbeatSeen.Load() {
					if time.Since(startedAt) > frontendWatchdogStartupGrace {
						a.exitBecauseFrontendLost("frontend heartbeat not started within grace period")
					}
					continue
				}
				lastHeartbeatAt := a.frontendHeartbeatAt.Load()
				if lastHeartbeatAt <= 0 {
					continue
				}
				lastHeartbeat := time.Unix(lastHeartbeatAt, 0)
				if time.Since(lastHeartbeat) > frontendWatchdogTimeout {
						a.exitBecauseFrontendLost(fmt.Sprintf("frontend heartbeat timeout: last=%s timeout=%s", lastHeartbeat.UTC().Format(time.RFC3339), frontendWatchdogTimeout))
					return
				}
			}
		}
	}()
}

func (a *App) stopFrontendWatchdog() {
	if a == nil {
		return
	}
	a.frontendWatchdogMu.Lock()
	stopCh := a.frontendWatchdogStop
	a.frontendWatchdogStop = nil
	a.frontendWatchdogMu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
}

func (a *App) exitBecauseFrontendLost(reason string) {
	if a == nil {
		os.Exit(0)
	}
	if !a.terminating.CompareAndSwap(false, true) {
		return
	}
	a.stopFrontendWatchdog()
	logManagerWarnf("frontend unavailable, manager exiting: %s", strings.TrimSpace(reason))
	_, _ = stopProbeLinkSession("frontend unavailable")
	if err := a.stopAIDebugServer(); err != nil {
		logManagerWarnf("failed to stop AI debug server during frontend-loss exit: %v", err)
	}
	if a.networkAssistant != nil {
		if err := a.networkAssistant.Shutdown(); err != nil {
			logManagerWarnf("failed to shutdown network assistant during frontend-loss exit: %v", err)
		}
	}
	os.Exit(0)
}

// Greet returns a greeting for the given name
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, It's show time!", name)
}

func (a *App) GetManagerVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimSpace(bi.Main.Version); v != "" && v != "(devel)" {
			return v
		}
	}

	if v := strings.TrimSpace(BuildVersion); v != "" && v != "(devel)" && !strings.EqualFold(v, "dev") {
		return v
	}

	return "dev"
}

func (a *App) GetLocalPrivateKeyStatus() PrivateKeyStatus {
	path, err := resolvePrivateKeyPath()
	if err != nil {
		return PrivateKeyStatus{
			Found:   false,
			Message: err.Error(),
		}
	}
	return PrivateKeyStatus{
		Found:   true,
		Path:    path,
		Message: "private key loaded",
	}
}

func (a *App) SignNonceWithLocalKey(nonce string) (string, error) {
	return signNonceWithLocalKey(nonce)
}

func signNonceWithLocalKey(nonce string) (string, error) {
	nonce = strings.TrimSpace(nonce)
	if nonce == "" {
		return "", errors.New("nonce is required")
	}

	path, err := resolvePrivateKeyPath()
	if err != nil {
		return "", err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read private key %s: %w", path, err)
	}

	block, _ := pem.Decode(raw)
	if block == nil {
		return "", fmt.Errorf("failed to decode pem private key: %s", path)
	}

	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %w", err)
	}

	priv, ok := keyAny.(ed25519.PrivateKey)
	if !ok {
		return "", errors.New("private key is not ed25519")
	}

	signature := ed25519.Sign(priv, []byte(nonce))
	return base64.StdEncoding.EncodeToString(signature), nil
}

func resolvePrivateKeyPath() (string, error) {
	candidates := []string{}

	if envPath := strings.TrimSpace(os.Getenv("CLOUDHELPER_ADMIN_PRIVATE_KEY_PATH")); envPath != "" {
		candidates = append(candidates, envPath)
	}

	candidates = append(candidates,
		filepath.Join(".", "data", "initial_admin_private_key.pem"),
		filepath.Join(".", "initial_admin_private_key.pem"),
	)

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		candidates = append(candidates, filepath.Join(home, ".cloudhelper", "initial_admin_private_key.pem"))
	}

	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", errors.New("local admin private key not found (set CLOUDHELPER_ADMIN_PRIVATE_KEY_PATH or place initial_admin_private_key.pem in ./data)")
}

func (a *App) ForceRefreshNetworkAssistantNodes(baseURL, token string) error {
	if a.networkAssistant == nil {
		return errors.New("network assistant not initialized")
	}
	a.networkAssistant.UpdateSession(baseURL, token)
	if err := a.networkAssistant.syncAvailableNodesFromController(); err != nil {
		return fmt.Errorf("force refresh network assistant nodes failed: %w", err)
	}
	return nil
}

func (a *App) AppendNetworkAssistantDebugLog(category, message string) error {
	if a.networkAssistant == nil {
		return errors.New("network assistant service is not initialized")
	}
	a.networkAssistant.logStore.Append(logSourceManager, category, strings.TrimSpace(message))
	return nil
}

func (a *App) GetProbeLinkChainsCache() ([]ProbeLinkChainCacheItem, error) {
	if a.networkAssistant == nil {
		return nil, errors.New("network assistant not initialized")
	}
	return a.networkAssistant.GetProbeLinkChainsCache()
}

func (a *App) applyAIDebugListenFromConfig() error {
	if a.aiDebugService == nil {
		return nil
	}
	return a.aiDebugService.ApplyFromConfig()
}

func (a *App) startAIDebugServer() error {
	if a.aiDebugService == nil {
		return nil
	}
	return a.aiDebugService.Start()
}

func (a *App) stopAIDebugServer() error {
	if a.aiDebugService == nil {
		return nil
	}
	return a.aiDebugService.Stop()
}
