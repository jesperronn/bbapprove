package app

import (
	"os"
	"testing"

	"bbapprove/internal/testfix"
)

func TestMain(m *testing.M) {
	helpGOOS = "linux" // the recorded help is the Unix text on every OS; TestHelpForWindows covers the other
	code := m.Run()
	testfix.Cleanup()
	os.Exit(code)
}
