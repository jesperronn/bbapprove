package adapter

import (
	"os"
	"testing"

	"bbapprove/internal/testfix"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testfix.Cleanup()
	os.Exit(code)
}
