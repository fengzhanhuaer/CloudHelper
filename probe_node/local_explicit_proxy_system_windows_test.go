//go:build windows

package main

import "testing"

func TestIsProbeLocalWindowsInteractiveUserSID(t *testing.T) {
	cases := []struct {
		sid  string
		want bool
	}{
		{sid: "S-1-5-21-1111111111-2222222222-3333333333-1001", want: true},
		{sid: "S-1-5-18", want: false},
		{sid: "S-1-5-19", want: false},
		{sid: "S-1-5-20", want: false},
		{sid: "S-1-5-21-1111111111-2222222222-3333333333-1001_Classes", want: false},
		{sid: ".DEFAULT", want: false},
		{sid: "", want: false},
	}
	for _, tc := range cases {
		if got := isProbeLocalWindowsInteractiveUserSID(tc.sid); got != tc.want {
			t.Fatalf("sid=%q got=%t want=%t", tc.sid, got, tc.want)
		}
	}
}
