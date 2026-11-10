// Package testfix holds fixed fake executables shared by the test suites. It is imported by tests
// only, never by production code.
//
// Writing a fresh script per test is slow on macOS: the first run of a newly written executable
// costs tens to hundreds of milliseconds. The scripts here are written once per test binary and
// steer their behaviour from environment variables, so each test keeps its own state (logs, bodies)
// and tests stay independent. The scripts are embedded in the Go source rather than checked in as
// testdata files so no test depends on a working directory or on a file's exec bit surviving a
// checkout or archive.
//
// Call Cleanup from TestMain to remove the directory the scripts were written to.
package testfix

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// adapterScript runs the shell snippet in $FAKE_BODY with the adapter's own arguments ($1, $2, ...).
const adapterScript = "#!/bin/sh\neval \"$FAKE_BODY\"\n"

// editorScript records its arguments to $FAKE_EDITOR_LOG, then runs $FAKE_EDITOR_BODY.
const editorScript = "#!/bin/sh\necho \"$*\" >> \"$FAKE_EDITOR_LOG\"\neval \"$FAKE_EDITOR_BODY\"\n"

var (
	once sync.Once
	dir  string
	err  error
)

func write() {
	once.Do(func() {
		dir, err = os.MkdirTemp("", "testfix-")
		if err != nil {
			return
		}
		for name, body := range map[string]string{"adapter": adapterScript, "editor": editorScript} {
			if err = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
				return
			}
		}
	})
}

func path(name string) string {
	write()
	if err != nil {
		panic("testfix: " + err.Error())
	}
	return filepath.Join(dir, name)
}

// Adapter is the path of the fake adapter. Set FAKE_BODY in the adapter's environment to a /bin/sh
// snippet; it runs with the adapter's arguments as $1, $2, ... and its exit status is the adapter's.
func Adapter() string { return path("adapter") }

// Editor is the path of the fake editor. Set FAKE_EDITOR_LOG (a file it appends its arguments to) and
// FAKE_EDITOR_BODY (a /bin/sh snippet, run with the editor's arguments) in the process environment.
func Editor() string { return path("editor") }

// Cleanup removes the directory holding the scripts; call it from TestMain after m.Run.
func Cleanup() {
	if dir != "" {
		os.RemoveAll(dir)
	}
}

// RequireShell skips t on Windows. The fake adapter and editor, and every test that writes a shell
// script, need /bin/sh, which Windows cannot run.
func RequireShell(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh scripts, which Windows cannot execute")
	}
}

// RequireUnixModes skips t on Windows, where files have no permission bits to assert on (every file
// reads back as 0666, directories as 0777, and nothing is "executable").
func RequireUnixModes(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs Unix file permission bits, which Windows does not have")
	}
}

// SetHome points the process's home directory at dir for the test: HOME on every OS plus USERPROFILE
// on Windows, which is what os.UserHomeDir reads there. It also clears XDG_CONFIG_HOME and, on
// Windows, points APPDATA and LocalAppData into dir so a per-user config never leaks in from the host.
func SetHome(t testing.TB, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
		t.Setenv("APPDATA", filepath.Join(dir, "AppData", "Roaming"))
		t.Setenv("LocalAppData", filepath.Join(dir, "AppData", "Local"))
	}
}
