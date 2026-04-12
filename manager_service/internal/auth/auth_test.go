package auth_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudhelper/manager_service/internal/auth"
)

func TestLoginSuccess(t *testing.T) {
	svc := newTestService(t)
	token, err := svc.Login("admin", "admin123")
	if err != nil {
		t.Fatalf("expected login success, got: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Login("admin", "wrong")
	if err != auth.ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got: %v", err)
	}
}

func TestLoginWrongUsername(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Login("root", "admin123")
	if err != auth.ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got: %v", err)
	}
}

func TestValidateToken(t *testing.T) {
	svc := newTestService(t)
	token, _ := svc.Login("admin", "admin123")

	if err := svc.ValidateToken(token); err != nil {
		t.Fatalf("expected valid token, got: %v", err)
	}
}

func TestValidateInvalidToken(t *testing.T) {
	svc := newTestService(t)
	_, _ = svc.Login("admin", "admin123")

	if err := svc.ValidateToken("bad-token"); err != auth.ErrUnauthenticated {
		t.Fatalf("expected ErrUnauthenticated, got: %v", err)
	}
}

func TestLogout(t *testing.T) {
	svc := newTestService(t)
	token, _ := svc.Login("admin", "admin123")
	svc.Logout(token)

	if err := svc.ValidateToken(token); err != auth.ErrUnauthenticated {
		t.Fatalf("expected ErrUnauthenticated after logout, got: %v", err)
	}
}

func TestChangeCredentials(t *testing.T) {
	svc := newTestService(t)
	// Change password.
	if err := svc.ChangeCredentials("admin123", "admin", "newpass456"); err != nil {
		t.Fatalf("ChangeCredentials failed: %v", err)
	}
	// Old password must fail.
	if _, err := svc.Login("admin", "admin123"); err != auth.ErrInvalidCredentials {
		t.Fatalf("old password should be rejected; got: %v", err)
	}
	// New password must succeed.
	if _, err := svc.Login("admin", "newpass456"); err != nil {
		t.Fatalf("new password login failed: %v", err)
	}
}

func TestChangeCredentialsWrongOldPassword(t *testing.T) {
	svc := newTestService(t)
	err := svc.ChangeCredentials("wrong", "admin", "newpass")
	if err != auth.ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got: %v", err)
	}
}

func TestChangeCredentialsInvalidatesSession(t *testing.T) {
	svc := newTestService(t)
	token, _ := svc.Login("admin", "admin123")
	_ = svc.ChangeCredentials("admin123", "admin", "newpass456")

	if err := svc.ValidateToken(token); err != auth.ErrUnauthenticated {
		t.Fatalf("old session should be invalidated; got: %v", err)
	}
}

func TestChangeUsername(t *testing.T) {
	svc := newTestService(t)
	if err := svc.ChangeCredentials("admin123", "newadmin", "admin123"); err != nil {
		t.Fatalf("ChangeCredentials (username) failed: %v", err)
	}
	if svc.CurrentUsername() != "newadmin" {
		t.Fatalf("expected username 'newadmin', got: %s", svc.CurrentUsername())
	}
	if _, err := svc.Login("newadmin", "admin123"); err != nil {
		t.Fatalf("login with new username failed: %v", err)
	}
}

func TestResetLocal(t *testing.T) {
	svc := newTestService(t)
	// Change to something non-default.
	_ = svc.ChangeCredentials("admin123", "admin", "changed")
	// Reset.
	if err := svc.ResetLocal(); err != nil {
		t.Fatalf("ResetLocal failed: %v", err)
	}
	// Default credentials must work again.
	if _, err := svc.Login("admin", "admin123"); err != nil {
		t.Fatalf("login with default creds after reset failed: %v", err)
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	svc1, _ := auth.NewService(dir)
	_ = svc1.ChangeCredentials("admin123", "admin", "persistent99")

	svc2, err := auth.NewService(dir)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if _, err := svc2.Login("admin", "persistent99"); err != nil {
		t.Fatalf("persisted password not found after reload: %v", err)
	}
}

// ---- helpers ----

func newTestService(t *testing.T) *auth.Service {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	svc, err := auth.NewService(dir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}
