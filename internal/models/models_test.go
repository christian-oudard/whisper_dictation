package models

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Selecting by menu number is the short way in, since the names run to
// twenty-odd characters.
func TestLookupByNumber(t *testing.T) {
	first, last := Catalog[0], Catalog[len(Catalog)-1]
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"1", first.Name, true},
		{strconv.Itoa(len(Catalog)), last.Name, true},
		{first.Name, first.Name, true},
		// Out of range, and the off-by-one that a 0-based reading would hit.
		{"0", "", false},
		{strconv.Itoa(len(Catalog) + 1), "", false},
		{"-1", "", false},
		{"", "", false},
		{"nope", "", false},
		// A path is not a menu entry, and must not be read as one.
		{"./3", "", false},
	} {
		got, ok := Lookup(c.in)
		if ok != c.ok || got.Name != c.want {
			t.Errorf("Lookup(%q) = %q,%v; want %q,%v", c.in, got.Name, ok, c.want, c.ok)
		}
	}
}

// Resolve has to agree with Lookup, or `diktat model 3` would check one
// model and switch to another.
func TestResolveByNumber(t *testing.T) {
	spec, _ := Lookup("2")
	if got := Resolve("2"); got != spec.Path() {
		t.Errorf("Resolve(\"2\") = %q, want %q", got, spec.Path())
	}
}

// A name that is also a number would be ambiguous. Nothing in the menu is,
// and names win if one ever is, but the catalog should not grow one by
// accident.
func TestNoNumericModelNames(t *testing.T) {
	for _, s := range Catalog {
		if _, err := strconv.Atoi(s.Name); err == nil {
			t.Errorf("%q is a number, which collides with menu-number selection", s.Name)
		}
	}
}

// The menu says a model is downloaded and the loader then refuses it: that is
// what a file with the right name and the wrong contents produces, and the
// library's own message for it is four words with no path in them.
func TestCheckReadsTheMagic(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.gguf")
	if err := os.WriteFile(good, append([]byte("GGUF"), 0, 0, 0, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Check(good); err != nil {
		t.Errorf("Check rejected a GGUF: %v", err)
	}

	for name, body := range map[string][]byte{
		"empty.gguf": {},
		"wrong.gguf": []byte("RIFF....WAVE"),
		"short.gguf": []byte("GG"),
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Check(path); err == nil {
			t.Errorf("Check accepted %s", name)
		}
	}
}
