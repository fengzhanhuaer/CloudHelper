package mobilecore

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMobileExternalSpeedRejectsUnknownActionAndUnroutableExit(t *testing.T) {
	var result map[string]any
	if err := json.Unmarshal([]byte(VRouteExternalSpeed("invalid", "")), &result); err != nil {
		t.Fatal(err)
	}
	if result["ok"] != false {
		t.Fatal(result)
	}
	t.Setenv("HOME", t.TempDir())
	client, cleanup, _, err := mobileExternalFactory(context.Background(), "missing-node")
	if cleanup != nil {
		cleanup()
	}
	if err == nil || client != nil {
		t.Fatal("unavailable exit silently became direct")
	}
}
