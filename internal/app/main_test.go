package app

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"bbapprove/internal/config"
	"bbapprove/internal/testfix"
)

// onWindows rewrites what is Windows-specific in output (backslashes, %PATH%, OS error texts) to the
// Unix form the recorded output uses. It is the identity on every other OS, so Unix comparisons are untouched.
func onWindows(s string) string {
	if runtime.GOOS != "windows" {
		return s
	}
	return strings.NewReplacer(`\`, "/", "%PATH%", "$PATH", "Incorrect function.", "is a directory").Replace(s)
}

func TestMain(m *testing.M) {
	config.HintGOOS = "linux" // likewise the recorded first-run hint
	helpGOOS = "linux"        // the recorded help is the Unix text on every OS; TestHelpForWindows covers the other
	code := m.Run()
	testfix.Cleanup()
	os.Exit(code)
}
