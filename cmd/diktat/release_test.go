package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The version lives in five places and they have to agree.
//
// Four of them are packaging -- the flake, the PKGBUILD, the Debian changelog,
// the RPM spec -- and the fifth is the ldflag that puts it in the binary. A release where
// they disagree produces packages that report a version they are not, which is
// the kind of mistake that is found by a user rather than by a maintainer, and
// then only when they report a bug against the wrong release.
//
// RELEASING.md says to bump all of them. This is why that instruction can be
// trusted to have been followed.
func TestVersionsAgree(t *testing.T) {
	root := repoRoot(t)
	found := map[string]string{
		"flake.nix version":      match(t, root, "flake.nix", `version\s*=\s*"([^"]+)"`),
		"flake.nix ldflag":       match(t, root, "flake.nix", `-X main\.version=([^\s"]+)`),
		"PKGBUILD pkgver":        match(t, root, "packaging/aur/PKGBUILD", `(?m)^pkgver=(\S+)`),
		"debian/changelog entry": match(t, root, "packaging/debian/changelog", `^diktat \(([^-)]+)`),
		"rpm spec Version":       match(t, root, "packaging/rpm/diktat.spec", `(?m)^Version:\s+(\S+)`),
		"changelog heading":      match(t, root, "CHANGELOG.md", `(?m)^## (\S+)`),
	}

	var first, from string
	for where, version := range found {
		if first == "" {
			first, from = version, where
			continue
		}
		if version != first {
			t.Errorf("%s says %s, %s says %s", from, first, where, version)
		}
	}
}

// A package that installs no unit file leaves the daemon with no way to start,
// one with no manual page is a binary in /usr/bin that answers only to --help,
// and one with no completion makes people type the model names out. All three
// are things to notice before a user does.
func TestPackagesShipTheirFiles(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"packaging/diktat.service", "packaging/diktat.1", "completions/_diktat"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("nothing to install: %v", err)
		}
	}
	for _, recipe := range []string{"packaging/aur/PKGBUILD", "packaging/debian/rules", "packaging/rpm/diktat.spec"} {
		body, err := os.ReadFile(filepath.Join(root, recipe))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`diktat\.service`, `diktat\.1`, `_diktat`} {
			if !regexp.MustCompile(want).Match(body) {
				t.Errorf("%s installs no %s", recipe, want)
			}
		}
	}
}

func match(t *testing.T, root, name, pattern string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	found := regexp.MustCompile(pattern).FindSubmatch(body)
	if found == nil {
		t.Fatalf("%s: nothing matched %s", name, pattern)
	}
	return string(found[1])
}

// repoRoot is where the packaging lives, two directories up from this test.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// The brackets on the version line are for a build whose source is not the
// release it names. Every packaging recipe stamps the tag, so repeating it
// there says nothing, and a tarball build has no revision to say.
func TestBuildDetail(t *testing.T) {
	for _, c := range []struct {
		version, rev, date, want string
	}{
		{"1.0.0", "v1.0.0", "", ""},
		{"1.0.0", "1.0.0", "", ""},
		{"1.0.0", "unknown", "", ""},
		{"dev", "unknown", "", ""},
		{"1.0.0", "abc1234", "", "abc1234"},
		{"1.0.0", "abc1234", "not a date", "abc1234"},
	} {
		if got := buildDetail(c.version, c.rev, c.date); got != c.want {
			t.Errorf("buildDetail(%q, %q, %q) = %q, want %q", c.version, c.rev, c.date, got, c.want)
		}
	}

	// A build date is worth showing even when the revision is the release
	// name. Rendered in the reader's timezone, so the day is what is checked
	// rather than the hour.
	stamped := buildDetail("1.0.0", "v1.0.0", "2026-08-20T12:00:00Z")
	if !strings.HasPrefix(stamped, "2026-08-") {
		t.Errorf("a tagged build with a date says %q", stamped)
	}
	both := buildDetail("1.0.0", "abc1234", "2026-08-20T12:00:00Z")
	if !strings.HasPrefix(both, "abc1234, 2026-08-") {
		t.Errorf("a revision with a date says %q", both)
	}
}
