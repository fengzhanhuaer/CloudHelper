package main

import (
	"errors"
	"testing"
)

func TestProbeRouteTCPConnTuningShouldLog(t *testing.T) {
	if probeRouteTCPConnTuningShouldLog("ok", nil, nil) {
		t.Fatal("successful socket tuning should be silent")
	}
	if !probeRouteTCPConnTuningShouldLog("socket_buffer_below_requested", nil, nil) {
		t.Fatal("non-ok socket tuning hint should be logged")
	}
	if !probeRouteTCPConnTuningShouldLog("ok", errors.New("set buffer failed")) {
		t.Fatal("socket tuning errors should be logged even when hint is ok")
	}
}
