package main

import (
	"crypto/subtle"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	probeLocalSetupTokenEnv  = "PROBE_LOCAL_SETUP_TOKEN"
	probeLocalSetupTokenFile = "probe_local_setup_token"
)

var probeLocalSetupTokenState struct {
	sync.Mutex
	token string
}

func ensureProbeLocalSetupToken() (string, error) {
	probeLocalSetupTokenState.Lock()
	defer probeLocalSetupTokenState.Unlock()
	if token := strings.TrimSpace(probeLocalSetupTokenState.token); token != "" {
		return token, nil
	}
	if token := strings.TrimSpace(os.Getenv(probeLocalSetupTokenEnv)); token != "" {
		if len(token) < 16 {
			return "", errors.New("PROBE_LOCAL_SETUP_TOKEN must contain at least 16 characters")
		}
		probeLocalSetupTokenState.token = token
		return token, nil
	}
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, probeLocalSetupTokenFile)
	if raw, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(raw)); len(token) >= 16 {
			if err := os.Chmod(path, 0o600); err != nil {
				return "", err
			}
			probeLocalSetupTokenState.token = token
			return token, nil
		}
	}
	token := randomHexToken(32)
	if token == "" {
		return "", errors.New("failed to generate probe local setup token")
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	probeLocalSetupTokenState.token = token
	logProbeInfof("probe local setup token created: path=%s", path)
	return token, nil
}

func verifyProbeLocalSetupToken(provided string) error {
	expected, err := ensureProbeLocalSetupToken()
	if err != nil {
		return err
	}
	provided = strings.TrimSpace(provided)
	if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		return &probeLocalHTTPError{Status: 403, Message: "invalid setup token"}
	}
	return nil
}

func consumeProbeLocalSetupToken() {
	probeLocalSetupTokenState.Lock()
	probeLocalSetupTokenState.token = ""
	probeLocalSetupTokenState.Unlock()
	if dataDir, err := resolveDataDir(); err == nil {
		_ = os.Remove(filepath.Join(dataDir, probeLocalSetupTokenFile))
	}
}

func resetProbeLocalSetupTokenForTest() {
	probeLocalSetupTokenState.Lock()
	probeLocalSetupTokenState.token = ""
	probeLocalSetupTokenState.Unlock()
}
