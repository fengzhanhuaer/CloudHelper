package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

var BuildVersion = "dev"

// App struct
type App struct {
	ctx              context.Context
	networkAssistant *networkAssistantService
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
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if err := cleanupManagerStaleExecutables(); err != nil {
		log.Printf("warning: failed to cleanup stale manager executable files: %v", err)
	}
	if err := autoBackupManagerData(); err != nil {
		log.Printf("warning: failed to backup manager data: %v", err)
	}
	a.networkAssistant.UpdateSession("", "")
}

func (a *App) shutdown(ctx context.Context) {
	if a.networkAssistant == nil {
		return
	}
	if err := a.networkAssistant.Shutdown(); err != nil {
		log.Printf("warning: failed to shutdown network assistant: %v", err)
	}
}

// Greet returns a greeting for the given name
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, It's show time!", name)
}

func (a *App) GetManagerVersion() string {
	if v := strings.TrimSpace(os.Getenv("CLOUDHELPER_MANAGER_VERSION")); v != "" {
		return v
	}

	for _, p := range managerVersionFileCandidates() {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(raw)); v != "" {
			return v
		}
	}

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

func managerVersionFileCandidates() []string {
	candidates := []string{
		filepath.Join(".", "version"),
		filepath.Join("..", "version"),
		filepath.Join("..", "..", "version"),
	}

	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "version"),
			filepath.Join(dir, "..", "version"),
			filepath.Join(dir, "..", "..", "version"),
		)
	}
	return candidates
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
