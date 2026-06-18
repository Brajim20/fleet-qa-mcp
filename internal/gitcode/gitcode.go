// Package gitcode runs read-only git queries against a Fleet checkout so code
// tools always analyze the *deployed* revision, not whatever branch is checked
// out. All functions take the repo dir resolved by fleetcfg.ResolveRepo.
package gitcode

import (
	"fmt"
	"os/exec"
	"strings"
)

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, string(out))
	}
	return string(out), nil
}

// Fetch updates remotes so a freshly-built/deployed revision is reachable.
// Note: locally-built revisions that were never pushed won't be fetchable —
// point FLEET_REPO at the build tree in that case.
func Fetch(repo string) error {
	_, err := git(repo, "fetch", "--all", "--quiet")
	return err
}

// HasRev reports whether a revision/commit is present in the local object store.
func HasRev(repo, rev string) bool {
	_, err := git(repo, "cat-file", "-t", rev)
	return err == nil
}

// ShowAtRev returns the contents of a file at a specific revision.
func ShowAtRev(repo, rev, path string) (string, error) {
	return git(repo, "show", fmt.Sprintf("%s:%s", rev, path))
}

// GrepAtRev greps for a pattern within a path at a revision (filenames+lines).
func GrepAtRev(repo, rev, pattern, pathspec string) (string, error) {
	out, err := git(repo, "grep", "-n", pattern, rev, "--", pathspec)
	// git grep exits 1 with no matches — treat as empty, not error.
	if err != nil && strings.TrimSpace(out) == "" {
		return "", nil
	}
	return out, nil
}

// IsAncestor reports whether commit is contained in rev's history — i.e.
// "is this PR-merge / cherry-pick in the deployed build?".
func IsAncestor(repo, commit, rev string) (bool, error) {
	cmd := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", commit, rev)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil // valid "not an ancestor"
	}
	return false, err // 128 = bad object, etc.
}

// LogSearch finds commits whose diff added/removed a string (for "when was X
// introduced / which PR").
func LogSearch(repo, ref, needle, pathspec string) (string, error) {
	args := []string{"log", ref, "--oneline", "-5", "-S", needle}
	if pathspec != "" {
		args = append(args, "--", pathspec)
	}
	return git(repo, args...)
}
