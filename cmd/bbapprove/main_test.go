package main

import "testing"

func TestWantsTUI(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"-i"}, true},
		{[]string{"--interactive", "--apply"}, true},
		{[]string{"--apply"}, false},
		{[]string{"-i", "--init"}, false},
		{[]string{"--init", "--force", "--interactive"}, false},
		{[]string{"-i", "--print-config"}, false},
	} {
		if got := wantsTUI(tc.args); got != tc.want {
			t.Errorf("wantsTUI(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
