package update

import (
	"context"
	"path/filepath"
	"strings"
)

// defaultBranch is the branch a checkout must be on to be updated.
const defaultBranch = "main"

// updateCheckout updates a git checkout in place: git fetch --tags, git pull --ff-only, bin/build.
// Preconditions (clean tracked files, on main, fast-forwardable) are checked before anything changes the
// working tree; a failed one leaves the repo as it was (the fetch only moves remote-tracking refs).
func (o Options) updateCheckout(ctx context.Context, res *Result) int {
	root := o.InstallRoot
	git := func(args ...string) ([]byte, error) {
		return o.Runner.Run(ctx, Cmd{Dir: root, Name: "git", Args: args})
	}
	fail := func(format string, a ...any) int {
		o.failln(format, a...)
		return 1
	}
	out, err := git("status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return fail("cannot inspect %s: %v", root, err)
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return fail("working tree %s has uncommitted changes; commit or stash them first (nothing changed):\n%s", root, s)
	}
	out, err = git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fail("cannot read the current branch in %s: %v", root, err)
	}
	if b := strings.TrimSpace(string(out)); b != defaultBranch {
		return fail("%s is on branch %q, not %q; run `git switch %s` first (nothing changed)", root, b, defaultBranch, defaultBranch)
	}

	o.outln("\n{h:Plan:} update the checkout {path:%s} in place ({ver:%s} -> {ver:%s})", root, res.Current.Raw, res.Latest.Tag)
	o.outln("  1. {cmd:git fetch --tags}")
	o.outln("  2. {cmd:git pull --ff-only}   {dim:(stops if main cannot be fast-forwarded)}")
	o.outln("  3. {cmd:bin/build}")
	if ok, code := o.proceed(); !ok {
		return code
	}

	stream := func(name string, args ...string) error {
		_, err := o.Runner.Run(ctx, Cmd{Dir: root, Name: name, Args: args, Stdout: o.Stdout, Stderr: o.Stderr})
		return err
	}
	if err := stream("git", "fetch", "--tags"); err != nil {
		return fail("git fetch --tags failed: %v", err)
	}
	if _, err := git("merge-base", "--is-ancestor", "HEAD", "@{upstream}"); err != nil {
		return fail("%s cannot be fast-forwarded to its upstream (local commits, diverged history or no upstream); not pulling, working tree untouched", defaultBranch)
	}
	if err := stream("git", "pull", "--ff-only"); err != nil {
		return fail("git pull --ff-only failed: %v", err)
	}
	if err := stream(filepath.Join(root, "bin", "build")); err != nil {
		return fail("bin/build failed: %v (the source is updated; fix the build and run bin/build)", err)
	}
	v, err := o.Runner.Run(ctx, Cmd{Dir: root, Name: filepath.Join(root, "bin", "bbapprove"), Args: []string{"--version"}})
	if err != nil {
		return fail("updated and built, but `bbapprove --version` failed: %v", err)
	}
	o.outln("{ok:Updated:} %s", strings.TrimSpace(string(v)))
	return 0
}
