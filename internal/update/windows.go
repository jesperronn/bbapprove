package update

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Windows installs are one folder holding bbapprove.exe and a few documents. There is no symlink to
// re-point and a running .exe cannot be overwritten, but it can be renamed. So the update replaces the
// files of the current folder in place: the running exe becomes bbapprove.exe.old, the new one takes its
// name, the documents are swapped, and any failure restores everything. User config lives in %AppData%
// and is never touched. The strategy is chosen by Options.GOOS and uses only portable file operations, so
// tests run it on any OS.

const (
	exeName   = "bbapprove.exe"
	oldSuffix = ".old"
	bakSuffix = ".bbapprove-bak"
)

// windowsDocs are replaced together with the exe when the release contains them.
var windowsDocs = []string{"bbapprove.config.template", "README.md", "CHANGELOG.md", "LICENSE"}

// installDir is the folder holding the running bbapprove.exe.
func (o Options) installDir() (string, error) {
	if o.ExeDir != "" {
		return o.ExeDir, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Dir(exe), nil
}

func (o Options) renameFn() func(a, b string) error {
	if o.rename != nil {
		return o.rename
	}
	return os.Rename
}

func (o Options) updateWindows(ctx context.Context, res *Result, asset string) int {
	tag := res.Latest.Tag
	fail := func(format string, a ...any) int {
		o.failln(format, a...)
		return 1
	}
	dir, err := o.installDir()
	if err != nil {
		return fail("cannot find the install folder: %v", err)
	}
	if st, err := os.Stat(filepath.Join(dir, exeName)); err != nil || !st.Mode().IsRegular() {
		return fail("%s holds no %s, so it does not look like a release install; nothing changed", dir, exeName)
	}
	body, url, code := o.releaseNotes(ctx, tag)
	if code != 0 {
		return code
	}

	o.outln("\n{h:Plan:} update {ver:%s} -> {ver:%s} in place (config in %%AppData%%\\bbapprove is not touched)", res.Current.Raw, tag)
	o.outln("  release notes ({path:%s}):\n%s", url, notes(body))
	o.outln("  {dim:asset:}   {path:%s} (+ SHA256SUMS, verified before unpacking)", asset)
	o.outln("  {dim:folder:}  {path:%s}", dir)
	o.outln("  {dim:then:}    {path:%s} is renamed to {path:%s%s}, the new one takes its place; any failure restores the old files", exeName, exeName, oldSuffix)
	if ok, code := o.proceed(); !ok {
		return code
	}

	parent := filepath.Dir(dir)
	tmp, err := os.MkdirTemp(parent, fmt.Sprintf(".bbapprove-update-%d-", o.Now().Unix()))
	if err != nil {
		return fail("cannot create a scratch directory in %s: %v", parent, err)
	}
	defer os.RemoveAll(tmp)
	if err := o.downloadVerified(ctx, tag, asset, tmp); err != nil {
		return fail("%v", err)
	}
	stage := filepath.Join(tmp, "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		return fail("%v", err)
	}
	top, err := ExtractZip(filepath.Join(tmp, "download", asset), stage, o.MaxExtract)
	if err != nil {
		return fail("refusing the archive: %v; nothing changed", err)
	}
	newDir := filepath.Join(stage, top)
	if st, err := os.Stat(filepath.Join(newDir, exeName)); err != nil || !st.Mode().IsRegular() {
		return fail("the release holds no %s; nothing changed", exeName)
	}
	if err := o.swapWindows(newDir, dir); err != nil {
		return fail("%v", err)
	}
	o.outln("\n{ok:Updated} {path:%s} to {ver:%s}; the new version starts the next time you run bbapprove; %s%s is the previous version (delete it when you no longer need it).", dir, tag, exeName, oldSuffix)
	return 0
}

// swapWindows moves the files of newDir into dir. Every replaced file is first renamed aside; if any
// step fails the renames are undone in reverse order, so dir is left as it was.
func (o Options) swapWindows(newDir, dir string) (err error) {
	rename := o.renameFn()
	var undo []func()
	defer func() {
		if err != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
			err = fmt.Errorf("%v; the previous files were restored, nothing changed", err)
		}
	}()
	// replace puts newPath at target, keeping what was there under aside until the whole swap succeeded.
	replace := func(newPath, target, aside string) error {
		if err := os.Remove(aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("cannot remove the old %s: %v", filepath.Base(aside), err)
		}
		hadOld := true
		if _, err := os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
			hadOld = false
		}
		if hadOld {
			if err := rename(target, aside); err != nil {
				return fmt.Errorf("cannot move %s aside (is another program using it?): %v", filepath.Base(target), err)
			}
			undo = append(undo, func() { os.Remove(target); _ = os.Rename(aside, target) })
		}
		if err := rename(newPath, target); err != nil {
			return fmt.Errorf("cannot put the new %s in place: %v", filepath.Base(target), err)
		}
		if !hadOld {
			undo = append(undo, func() { os.Remove(target) })
		}
		return nil
	}
	exe := filepath.Join(dir, exeName)
	if err := replace(filepath.Join(newDir, exeName), exe, exe+oldSuffix); err != nil {
		return err
	}
	var asides []string
	for _, name := range windowsDocs {
		src := filepath.Join(newDir, name)
		if st, e := os.Stat(src); e != nil || !st.Mode().IsRegular() {
			continue
		}
		target := filepath.Join(dir, name)
		if err := replace(src, target, target+bakSuffix); err != nil {
			return err
		}
		asides = append(asides, target+bakSuffix)
	}
	for _, a := range asides { // the swap succeeded; only bbapprove.exe.old stays for the user
		os.Remove(a)
	}
	return nil
}
