package mobilecore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestResolveWebSocketURL(t *testing.T) {
	got, err := resolveWebSocketURL("https://controller.example.com:15030/admin")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://controller.example.com:15030/api/probe" {
		t.Fatalf("ws url=%q", got)
	}
}

func TestSignConnect(t *testing.T) {
	got := signConnect("secret-1", "node-1", "100", "abc")
	mac := hmac.New(sha256.New, []byte("secret-1"))
	_, _ = mac.Write([]byte("node-1\n100\nabc"))
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("signature=%q want %q", got, want)
	}
}
