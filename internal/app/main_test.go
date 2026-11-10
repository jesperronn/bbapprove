package app

import (
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"bbapprove/internal/config"
	"bbapprove/internal/testfix"
)

// onWindows rewrites what is Windows-specific in output (backslashes, %PATH%, OS error texts) to the
// Unix form the recorded output uses. It is the identity on every other OS, so Unix comparisons are untouched.
var quotedPlaceholder = regexp.MustCompile(`'(\$[A-Z]+[^']*)'`)

func onWindows(s string) string {
	if runtime.GOOS != "windows" {
		return s
	}
	s = strings.NewReplacer(`\`, "/", "%PATH%", "$PATH", "Incorrect function.", "is a directory").Replace(s)
	// a Windows path contains characters that make the Unix hint quote it; the recorded text has none
	return quotedPlaceholder.ReplaceAllString(s, "$1")
}

func TestMain(m *testing.M) {
	config.HintGOOS = "linux" // likewise the recorded first-run hint
	helpGOOS = "linux"        // the recorded help is the Unix text on every OS; TestHelpForWindows covers the other
	code := m.Run()
	testfix.Cleanup()
	os.Exit(code)
}
