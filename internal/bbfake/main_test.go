package bbfake_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// goBinary is the native Go adapter (cmd/bbapprove-bitbucket), built once for the characterization suite.
var goBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bbapprove-bitbucket-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	goBinary = filepath.Join(dir, "bbapprove-bitbucket")
	if runtime.GOOS == "windows" {
		goBinary += ".exe"
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goTool); err != nil {
		goTool = "go"
	}
	build := exec.Command(goTool, "build", "-o", goBinary, "../../cmd/bbapprove-bitbucket")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the Go adapter: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
