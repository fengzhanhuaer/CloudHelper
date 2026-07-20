package core

import (
	"crypto/subtle"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	mngSetupTokenEnv  = "PROBE_CONTROLLER_SETUP_TOKEN"
	mngSetupTokenFile = "mng_setup_token"
)

var mngSetupTokenState struct {
	sync.Mutex
	token string
}

func ensureMngSetupToken() (string, error) {
	mngSetupTokenState.Lock()
	defer mngSetupTokenState.Unlock()
	if token := strings.TrimSpace(mngSetupTokenState.token); token != "" {
		return token, nil
	}
	if token := strings.TrimSpace(os.Getenv(mngSetupTokenEnv)); token != "" {
		if len(token) < 16 {
			return "", fmt.Errorf("%s must contain at least 16 characters", mngSetupTokenEnv)
		}
		mngSetupTokenState.token = token
		return token, nil
	}
	path := filepath.Join(dataDir, mngSetupTokenFile)
	if raw, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(raw)); len(token) >= 16 {
			if err := os.Chmod(path, 0o600); err != nil {
				return "", err
			}
			mngSetupTokenState.token = token
			return token, nil
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	mngSetupTokenState.token = token
	log.Printf("management setup token created: path=%s", path)
	return token, nil
}

func verifyMngSetupToken(provided string) error {
	expected, err := ensureMngSetupToken()
	if err != nil {
		return err
	}
	provided = strings.TrimSpace(provided)
	if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		return &mngHTTPError{Status: 403, Message: "invalid setup token"}
	}
	return nil
}

func consumeMngSetupToken() {
	mngSetupTokenState.Lock()
	mngSetupTokenState.token = ""
	mngSetupTokenState.Unlock()
	_ = os.Remove(filepath.Join(dataDir, mngSetupTokenFile))
}

func resetMngSetupTokenForTest() {
	mngSetupTokenState.Lock()
	mngSetupTokenState.token = ""
	mngSetupTokenState.Unlock()
}
