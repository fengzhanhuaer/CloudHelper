// Package auth implements single-account username/password authentication
// with bcrypt password hashing and in-memory session token management.
// RQ-005: single-account username/password login.
// RQ-006: change username/password after login.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	credentialFile    = "manager_credentials.json"
	defaultUsername   = "admin"
	defaultPassword   = "admin123"
	bcryptCost        = 12
	sessionTokenBytes = 32
	sessionTTL        = 8 * time.Hour
)

// ErrInvalidCredentials is returned when login credentials do not match.
var ErrInvalidCredentials = errors.New("invalid username or password")

// ErrUnauthenticated is returned when a session token is missing or expired.
var ErrUnauthenticated = errors.New("unauthenticated")

// credentials holds the persisted single-account credential record.
type credentials struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

// sessionEntry holds an active session token with its expiry.
type sessionEntry struct {
	Token     string
	ExpiresAt time.Time
}

// Service manages authentication state for manager_service.
type Service struct {
	mu      sync.RWMutex
	dataDir string
	cred    credentials
	session *sessionEntry // only one active session at a time
}

// NewService initialises the auth service and loads credentials from disk.
// If no credential file exists, default credentials are created and persisted.
func NewService(dataDir string) (*Service, error) {
	s := &Service{dataDir: dataDir}
	if err := s.loadOrInit(); err != nil {
		return nil, fmt.Errorf("auth init: %w", err)
	}
	return s, nil
}

// Login validates username and password, issues a new session token on success.
func (s *Service) Login(username, password string) (token string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if username != s.cred.Username {
		return "", ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(s.cred.PasswordHash), []byte(password)); err != nil {
		return "", ErrInvalidCredentials
	}

	token, err = generateToken()
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	s.session = &sessionEntry{
		Token:     token,
		ExpiresAt: time.Now().Add(sessionTTL),
	}
	return token, nil
}

// Logout invalidates the current session.
func (s *Service) Logout(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil && s.session.Token == token {
		s.session = nil
	}
}

// ValidateToken checks whether the given token matches the active session
// and has not expired. Returns ErrUnauthenticated if invalid.
func (s *Service) ValidateToken(token string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.session == nil {
		return ErrUnauthenticated
	}
	if s.session.Token != token {
		return ErrUnauthenticated
	}
	if time.Now().After(s.session.ExpiresAt) {
		return ErrUnauthenticated
	}
	return nil
}

// CurrentUsername returns the username of the single managed account.
func (s *Service) CurrentUsername() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cred.Username
}

// ChangeCredentials updates username and/or password for the managed account.
// oldPassword must match the current password. The active session is invalidated
// so the user must re-login after the change.
// RQ-006.
func (s *Service) ChangeCredentials(oldPassword, newUsername, newPassword string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := bcrypt.CompareHashAndPassword([]byte(s.cred.PasswordHash), []byte(oldPassword)); err != nil {
		return ErrInvalidCredentials
	}
	if newUsername == "" {
		newUsername = s.cred.Username
	}
	if newPassword == "" {
		return errors.New("new password must not be empty")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash new password: %w", err)
	}
	s.cred = credentials{Username: newUsername, PasswordHash: string(hash)}
	if err := s.persist(); err != nil {
		return fmt.Errorf("persist credentials: %w", err)
	}
	// Invalidate active session — force re-login.
	s.session = nil
	return nil
}

// ResetLocal resets credentials to default without requiring the old password.
// This is only reachable from localhost (enforced at the HTTP layer).
func (s *Service) ResetLocal() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	hash, err := bcrypt.GenerateFromPassword([]byte(defaultPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash default password: %w", err)
	}
	s.cred = credentials{Username: defaultUsername, PasswordHash: string(hash)}
	if err := s.persist(); err != nil {
		return fmt.Errorf("persist reset credentials: %w", err)
	}
	s.session = nil
	return nil
}

// ---- internal ----

func (s *Service) loadOrInit() error {
	path := s.credPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s.initDefault()
		}
		return fmt.Errorf("read credentials: %w", err)
	}
	var cred credentials
	if err := json.Unmarshal(raw, &cred); err != nil {
		return fmt.Errorf("parse credentials: %w", err)
	}
	if cred.Username == "" || cred.PasswordHash == "" {
		return s.initDefault()
	}
	s.cred = cred
	return nil
}

func (s *Service) initDefault() error {
	hash, err := bcrypt.GenerateFromPassword([]byte(defaultPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash default password: %w", err)
	}
	s.cred = credentials{Username: defaultUsername, PasswordHash: string(hash)}
	return s.persist()
}

func (s *Service) persist() error {
	raw, err := json.MarshalIndent(s.cred, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(s.credPath(), raw, 0o600) // owner-read-only
}

func (s *Service) credPath() string {
	return filepath.Join(s.dataDir, credentialFile)
}

func generateToken() (string, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
