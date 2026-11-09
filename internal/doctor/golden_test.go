package doctor

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files in testdata/")

// normalize replaces the scratch directories so the output is stable across runs.
func (r *rig) normalize(s string) string {
	return strings.NewReplacer(r.root, "$ROOT", r.xdg, "$XDG").Replace(s)
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file (run: go test ./internal/doctor -run Golden -update)\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// goldenRig is a healthy scratch install with an ignore list, disallow lists and BBAPPROVE_* variables.
func goldenRig(t *testing.T) *rig {
	r := newRig(t)
	r.writeXDG(validCfg)
	os.WriteFile(r.ignorePath(), []byte("left-pad\nlodash\n"), 0o600)
	os.WriteFile(filepath.Join(r.xdg, "bbapprove", "config.toml"), []byte(validCfg), 0o600)
	r.env = append(r.env, "BBAPPROVE_JOBS=3", "BBAPPROVE_DISALLOW=Foo,bar", "EDITOR=vi")
	return r
}

func TestGoldenPlainDoctor(t *testing.T) {
	r := goldenRig(t)
	_, out := r.doctor(func(o *Options) { o.Offline = true })
	checkGolden(t, "doctor_ok.golden", r.normalize(out))
}

func TestGoldenPlainDoctorUpdateAvailable(t *testing.T) {
	r := goldenRig(t)
	r.run.releases = `[{"tagName":"v1.2.0","isPrerelease":false,"isDraft":false}]`
	_, out := r.doctor(nil)
	checkGolden(t, "doctor_update.golden", r.normalize(out))
}

func TestGoldenPlainDoctorProblems(t *testing.T) {
	r := newRig(t)
	r.env = []string{"NO_COLOR=1", "XDG_CONFIG_HOME=" + r.xdg}
	r.run.releaseEr = os.ErrDeadlineExceeded
	os.MkdirAll(filepath.Join(r.xdg, "bbapprove"), 0o755)
	os.WriteFile(r.ignorePath(), []byte("x\n"), 0o600)
	os.Remove(filepath.Join(r.root, "adapters", "bitbucket"))
	_, out := r.doctor(nil)
	checkGolden(t, "doctor_problems.golden", r.normalize(out))
}
