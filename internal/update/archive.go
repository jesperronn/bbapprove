package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// Archive installs are updated SIDE BY SIDE: the new release is unpacked into a new sibling directory
// and the running tree is never modified, so rolling back is re-pointing one symlink.
//
// Honest limitation: SHA256SUMS is downloaded from the same release as the archive. The check protects
// against corruption and partial downloads, NOT against a compromised release or account. Build
// provenance / attestation verification is future work.

// AssetName is the release asset for a platform. Only the platforms the release workflow builds are
// supported.
func AssetName(tag, goos, goarch string) (string, error) {
	if (goos != "darwin" && goos != "linux") || (goarch != "amd64" && goarch != "arm64") {
		return "", fmt.Errorf("unsupported platform %s/%s: releases are built for darwin and linux on amd64 and arm64 only", goos, goarch)
	}
	return fmt.Sprintf("bbapprove-%s-%s-%s.tar.gz", tag, goos, goarch), nil
}

const notesLines = 25

func (o Options) updateArchive(ctx context.Context, res *Result) int {
	tag := res.Latest.Tag
	fail := func(format string, a ...any) int {
		fmt.Fprintf(o.Stderr, "bbapprove: "+format+"\n", a...)
		return 1
	}
	asset, err := AssetName(tag, o.GOOS, o.GOARCH)
	if err != nil {
		fmt.Fprintf(o.Stderr, "bbapprove: %v\n", err)
		return 2
	}
	oldRoot := o.InstallRoot
	parent := filepath.Dir(oldRoot)
	dest := filepath.Join(parent, strings.TrimSuffix(asset, ".tar.gz"))
	if _, err := os.Lstat(dest); err == nil {
		return fail("%s already exists; move or remove it first (nothing changed)", dest)
	}

	body, url := "", o.releasesURL()+"/tag/"+tag
	out, err := o.capture(ctx, Cmd{Name: "gh", Args: []string{"release", "view", tag, "--repo", o.Slug, "--json", "body,url"}})
	switch {
	case errors.Is(err, exec.ErrNotFound):
		o.manualSteps(tag)
		return 2
	case err != nil:
		fmt.Fprintf(o.Stderr, "bbapprove: cannot read release %s: %v\n", tag, err)
		return 2
	}
	var rv struct{ Body, URL string }
	if err := json.Unmarshal(out, &rv); err != nil {
		fmt.Fprintf(o.Stderr, "bbapprove: unexpected gh output for release %s: %v\n", tag, err)
		return 2
	}
	body = rv.Body
	if rv.URL != "" {
		url = rv.URL
	}

	fmt.Fprintf(o.Stdout, "\nPlan: install %s -> %s side by side (the running install is not modified)\n", res.Current.Raw, tag)
	fmt.Fprintf(o.Stdout, "  release notes (%s):\n%s\n", url, notes(body))
	fmt.Fprintf(o.Stdout, "  asset:   %s (+ SHA256SUMS, verified before unpacking)\n", asset)
	fmt.Fprintf(o.Stdout, "  into:    %s   (new directory next to %s)\n", dest, oldRoot)
	fmt.Fprintln(o.Stdout, "  then:    nothing is switched automatically; the commands to switch and to roll back are printed")
	if ok, code := o.proceed(); !ok {
		return code
	}

	tmp, err := os.MkdirTemp(parent, fmt.Sprintf(".bbapprove-update-%d-", o.Now().Unix()))
	if err != nil {
		return fail("cannot create a scratch directory in %s: %v", parent, err)
	}
	defer os.RemoveAll(tmp)
	dl := filepath.Join(tmp, "download")
	if err := os.Mkdir(dl, 0o700); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(o.Stdout, "Downloading %s ...\n", asset)
	if _, err := o.Runner.Run(ctx, Cmd{Name: "gh", Args: []string{"release", "download", tag, "--repo", o.Slug,
		"-p", asset, "-p", "SHA256SUMS", "-D", dl}}); err != nil {
		return fail("download failed: %v (nothing changed)", err)
	}
	if err := VerifyChecksum(filepath.Join(dl, asset), filepath.Join(dl, "SHA256SUMS"), asset); err != nil {
		return fail("%v; aborting, download deleted, nothing changed", err)
	}
	fmt.Fprintln(o.Stdout, "Checksum OK. Unpacking ...")
	f, err := os.Open(filepath.Join(dl, asset))
	if err != nil {
		return fail("%v", err)
	}
	defer f.Close()
	stage := filepath.Join(tmp, "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		return fail("%v", err)
	}
	top, err := ExtractTarGz(f, stage, o.MaxExtract)
	if err != nil {
		return fail("refusing the archive: %v; nothing changed", err)
	}
	if _, err := os.Lstat(dest); err == nil { // lost a race since the plan
		return fail("%s already exists; nothing changed", dest)
	}
	if err := os.Rename(filepath.Join(stage, top), dest); err != nil {
		return fail("cannot move the new tree into place: %v", err)
	}
	o.printSwitch(oldRoot, dest)
	return 0
}

// notes truncates the release body to notesLines lines, drops terminal control characters (the text is
// untrusted) and indents it.
func notes(body string) string {
	body = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1 // includes ESC and CR
		}
		return r
	}, body)
	body = strings.TrimSpace(body)
	if body == "" {
		return "    (no release notes)"
	}
	lines := strings.Split(body, "\n")
	more := len(lines) > notesLines
	if more {
		lines = lines[:notesLines]
	}
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	if more {
		lines = append(lines, "    ... (truncated, see the release page)")
	}
	return strings.Join(lines, "\n")
}

// manualSteps explains how to do the archive update by hand when gh is not available.
func (o Options) manualSteps(tag string) {
	asset := "bbapprove-" + tag + "-<os>-<arch>.tar.gz"
	if a, err := AssetName(tag, o.GOOS, o.GOARCH); err == nil {
		asset = a
	}
	fmt.Fprintf(o.Stdout, `
The GitHub CLI (gh) is not installed, so bbapprove cannot download the release for you. Manually:
  1. open %s/latest and note the tag (%s)
  2. download %s and SHA256SUMS from that release page into an empty directory
  3. verify:  shasum -a 256 -c SHA256SUMS --ignore-missing
  4. extract: tar -xzf %s   (creates a new directory; do not unpack over the current install %s)
  5. switch by re-pointing your bbapprove symlink to <new>/bin/bbapprove; keep the old tree for rollback
Or install gh (https://cli.github.com), run `+"`gh auth login`"+` and re-run bbapprove --update.
`, o.releasesURL(), tag, asset, asset, o.InstallRoot)
}

// printSwitch tells where the new tree is and how to switch to it and back. It never switches itself.
func (o Options) printSwitch(oldRoot, dest string) {
	w := o.Stdout
	fmt.Fprintf(w, "\nInstalled the new release into %s\n", dest)
	fmt.Fprintln(w, "Config: $XDG_CONFIG_HOME/bbapprove/config.toml (default ~/.config/bbapprove/config.toml) is untouched and shared by both trees.")
	oldCfg := filepath.Join(oldRoot, "bbapprove.config.toml")
	if _, err := os.Stat(oldCfg); err == nil {
		fmt.Fprintf(w, "The old tree has its own %s: copy it into the new tree (cp %s %s/) or into the XDG path above.\n", oldCfg, oldCfg, dest)
	}
	link, mapped := o.findSymlink(oldRoot)
	if link == "" {
		fmt.Fprintf(w, "To switch: point your bbapprove command at %s/bin/bbapprove (ln -sfn %s/bin/bbapprove <path of your symlink>).\n", dest, dest)
		fmt.Fprintf(w, "To roll back: point it at %s/bin/bbapprove again. The old tree is kept at %s.\n", oldRoot, oldRoot)
		return
	}
	newTarget := filepath.Join(dest, mapped)
	if _, err := os.Stat(newTarget); err != nil {
		newTarget = filepath.Join(dest, "bin", "bbapprove")
	}
	fmt.Fprintf(w, "To switch:    ln -sfn %s %s\n", newTarget, link)
	fmt.Fprintf(w, "To roll back: ln -sfn %s %s\n", filepath.Join(oldRoot, mapped), link)
	fmt.Fprintf(w, "The old tree is kept at %s.\n", oldRoot)
}

// findSymlink looks up `bbapprove` on PATH; when it is a symlink resolving into oldRoot it returns the
// link path and the target's path relative to oldRoot.
func (o Options) findSymlink(oldRoot string) (link, rel string) {
	realRoot, err := filepath.EvalSymlinks(oldRoot)
	if err != nil {
		realRoot = oldRoot
	}
	for _, dir := range filepath.SplitList(o.PathEnv) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, "bbapprove")
		st, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if st.Mode()&fs.ModeSymlink == 0 {
			return "", "" // first hit on PATH is a regular file: not ours to switch
		}
		target, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", ""
		}
		r, err := filepath.Rel(realRoot, target)
		if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", ""
		}
		return p, r
	}
	return "", ""
}

// VerifyChecksum checks the SHA-256 of file against the line for name in the sums file ("<hex>  <name>"
// or "<hex> *<name>"). A missing line is an error.
func VerifyChecksum(file, sums, name string) error {
	b, err := os.ReadFile(sums)
	if err != nil {
		return fmt.Errorf("cannot read SHA256SUMS: %v", err)
	}
	want := ""
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if strings.TrimPrefix(strings.TrimPrefix(f[1], "*"), "./") == name {
			want = strings.ToLower(f[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("SHA256SUMS has no entry for %s", name)
	}
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("cannot read the downloaded asset: %v", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("cannot hash the downloaded asset: %v", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", name, want, got)
	}
	return nil
}

const maxEntries = 50000

// within reports whether p (slash-separated, cleaned, relative) is top or below it.
func within(p, top string) bool { return p == top || strings.HasPrefix(p, top+"/") }

// ExtractTarGz unpacks a .tar.gz into dest (which must exist and should be empty) and returns the name
// of the single top-level directory every entry lives under. It rejects absolute paths, "..", entries
// outside the top directory, symlinks or hardlinks pointing outside it, writes through symlinks,
// special files, duplicates, and archives that expand beyond maxBytes. setuid/setgid/sticky bits are
// dropped.
func ExtractTarGz(r io.Reader, dest string, maxBytes int64) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("not a gzip file: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var top string
	var left = maxBytes
	var dirModes []struct {
		p string
		m fs.FileMode
	}
	var links []string
	files := map[string]bool{} // regular files extracted, for hardlink targets
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("corrupt archive: %v", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if n++; n > maxEntries {
			return "", fmt.Errorf("too many entries (more than %d)", maxEntries)
		}
		name := h.Name
		if name == "" || strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') || path.IsAbs(name) {
			return "", fmt.Errorf("unsafe entry name %q", name)
		}
		clean := path.Clean(name)
		if clean == "." {
			continue // "./" root entry
		}
		if clean == ".." || strings.HasPrefix(clean, "../") {
			return "", fmt.Errorf("entry %q escapes the archive", name)
		}
		first, _, nested := strings.Cut(clean, "/")
		if top == "" {
			if !nested && h.Typeflag != tar.TypeDir {
				return "", fmt.Errorf("entry %q is outside a top-level directory", name)
			}
			top = first
		} else if first != top {
			return "", fmt.Errorf("entry %q is outside the top directory %q", name, top)
		}
		target := filepath.Join(dest, filepath.FromSlash(clean))
		mode := fs.FileMode(h.Mode) & 0o777 // never setuid/setgid/sticky

		// Never write through a symlink that an earlier entry planted.
		if err := noSymlinkParents(dest, filepath.Dir(target)); err != nil {
			return "", fmt.Errorf("entry %q: %v", name, err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
			dirModes = append(dirModes, struct {
				p string
				m fs.FileMode
			}{target, mode | 0o700})
		case tar.TypeReg:
			if h.Size < 0 || h.Size > left {
				return "", fmt.Errorf("archive expands beyond the %d MiB limit", maxBytes>>20)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode|0o600)
			if err != nil {
				return "", fmt.Errorf("entry %q: %v", name, err)
			}
			c, err := io.Copy(f, io.LimitReader(tr, h.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return "", fmt.Errorf("entry %q: %v", name, err)
			}
			left -= c
			if err := os.Chmod(target, mode|0o600); err != nil {
				return "", err
			}
			files[clean] = true
		case tar.TypeSymlink:
			lt := h.Linkname
			if lt == "" || path.IsAbs(lt) || strings.ContainsRune(lt, 0) || strings.ContainsRune(lt, '\\') {
				return "", fmt.Errorf("symlink %q has unsafe target %q", name, lt)
			}
			if resolved := path.Join(path.Dir(clean), lt); !within(resolved, top) {
				return "", fmt.Errorf("symlink %q points outside the install (%q)", name, lt)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			if err := os.Symlink(lt, target); err != nil {
				return "", fmt.Errorf("entry %q: %v", name, err)
			}
			links = append(links, target)
		case tar.TypeLink:
			lt := path.Clean(h.Linkname)
			if h.Linkname == "" || path.IsAbs(h.Linkname) || !within(lt, top) || !files[lt] {
				return "", fmt.Errorf("hardlink %q points outside the install or to an unknown file (%q)", name, h.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			src := filepath.Join(dest, filepath.FromSlash(lt))
			st, err := os.Stat(src)
			if err != nil {
				return "", err
			}
			if left -= st.Size(); left < 0 {
				return "", fmt.Errorf("archive expands beyond the %d MiB limit", maxBytes>>20)
			}
			// Copy instead of hardlinking: no shared inode with anything else.
			if err := copyFile(src, target, mode|0o600); err != nil {
				return "", fmt.Errorf("entry %q: %v", name, err)
			}
			files[clean] = true
		default:
			return "", fmt.Errorf("entry %q has unsupported type %q", name, string(h.Typeflag))
		}
	}
	if top == "" {
		return "", errors.New("archive is empty")
	}
	// Symlink chains can compose into an escape that each link alone does not show: resolve them for real.
	realTop, err := filepath.EvalSymlinks(filepath.Join(dest, top))
	if err != nil {
		return "", err
	}
	for _, l := range links {
		rp, err := filepath.EvalSymlinks(l)
		if errors.Is(err, fs.ErrNotExist) {
			continue // dangling inside the tree: harmless
		}
		if err != nil {
			return "", fmt.Errorf("symlink %s: %v", l, err)
		}
		if rel, err := filepath.Rel(realTop, rp); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("symlink %s resolves outside the install", l)
		}
	}
	for i := len(dirModes) - 1; i >= 0; i-- {
		if err := os.Chmod(dirModes[i].p, dirModes[i].m); err != nil {
			return "", err
		}
	}
	return top, nil
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// noSymlinkParents fails when any existing component of dir below root is a symlink.
func noSymlinkParents(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return err
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("would write through the symlink %s", cur)
		}
	}
	return nil
}
