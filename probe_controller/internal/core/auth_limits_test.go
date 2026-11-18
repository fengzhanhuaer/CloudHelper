package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func setupLoginLimitsTest(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	previous := authManager
	t.Cleanup(func() { authManager = previous })
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authManager = &AuthManager{
		adminPublicKey: public, nonces: map[string]time.Time{}, sessions: map[string]time.Time{},
		nonceRequestFailed: map[string]int{}, blacklist: &BlacklistStore{cidrs: map[string]struct{}{}},
	}
	return private
}

type loginCountingReader struct {
	io.Reader
	read int
}

func (r *loginCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func TestLoginRejectsOversizedBodiesBeforeAuthentication(t *testing.T) {
	setupLoginLimitsTest(t)
	for _, body := range []string{
		`{"nonce":"missing","signature":"` + strings.Repeat("a", 2*loginBodyMaxBytes) + `"}`,
		`{"extra":"` + strings.Repeat("a", 2*loginBodyMaxBytes) + `"}`,
		`{"nonce":"missing","signature":"a"}` + strings.Repeat(" ", 2*loginBodyMaxBytes),
	} {
		reader := &loginCountingReader{Reader: strings.NewReader(body)}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", reader)
		req.ContentLength = -1 // Do not rely on Content-Length or proxy checks.
		rec := httptest.NewRecorder()
		LoginHandler(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
		}
		if reader.read > loginBodyMaxBytes+1 {
			t.Fatalf("read %d bytes before rejecting oversized body", reader.read)
		}
	}
}

func TestLoginLimitsPreserveSignedAuthentication(t *testing.T) {
	private := setupLoginLimitsTest(t)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		nonce, err := randomHex(32)
		if err != nil {
			t.Fatal(err)
		}
		authManager.AddNonceForTest(nonce, time.Now().Add(time.Minute))
		body, err := json.Marshal(LoginRequest{
			Nonce: nonce, Signature: encoding.EncodeToString(ed25519.Sign(private, []byte(nonce))),
		})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		LoginHandler(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(body))))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "session_token") {
			t.Fatalf("signed login failed: status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}

func TestLoginRejectsLongFieldsAndTrailingJSON(t *testing.T) {
	setupLoginLimitsTest(t)
	for _, body := range []string{
		`{"nonce":"` + strings.Repeat("n", 129) + `","signature":"s"}`,
		`{"nonce":"n","signature":"` + strings.Repeat("s", 257) + `"}`,
		`{"nonce":"n","signature":"s"} {}`,
	} {
		rec := httptest.NewRecorder()
		LoginHandler(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	}
}

func TestLoginConcurrencyBudgetRejectsWithoutReading(t *testing.T) {
	setupLoginLimitsTest(t)
	for i := 0; i < cap(loginRequests); i++ {
		loginRequests <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(loginRequests); i++ {
			<-loginRequests
		}
	}()
	reader := &loginCountingReader{Reader: strings.NewReader(`{}`)}
	rec := httptest.NewRecorder()
	LoginHandler(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", reader))
	if rec.Code != http.StatusServiceUnavailable || reader.read != 0 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("busy login: status=%d read=%d headers=%v", rec.Code, reader.read, rec.Header())
	}
}
