package tui

import (
	"reflect"
	"testing"
)

func TestOpenCommand(t *testing.T) {
	const url = "https://bb.example.com/projects/P/repos/r/pull-requests/1?a=1&b=2"
	for _, tc := range []struct {
		goos, name string
		args       []string
	}{
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", url}},
		{"darwin", "open", []string{url}},
		{"linux", "xdg-open", []string{url}},
		{"freebsd", "xdg-open", []string{url}},
	} {
		name, args := OpenCommand(tc.goos, url)
		if name != tc.name || !reflect.DeepEqual(args, tc.args) {
			t.Errorf("%s: OpenCommand = %q %q, want %q %q", tc.goos, name, args, tc.name, tc.args)
		}
	}
}
