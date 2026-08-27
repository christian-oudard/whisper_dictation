package xdg

import (
	"path/filepath"
	"testing"
)

// XDG_STATE_HOME is the tier for "state that persists between restarts", and
// is what the spec says to honour before falling back to ~/.local/state.
func TestStateDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/somewhere/state")
	if got, want := StateDir(), "/somewhere/state/diktat"; got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "")
	if got := StateDir(); filepath.Base(got) != "diktat" ||
		filepath.Base(filepath.Dir(got)) != "state" {
		t.Errorf("StateDir() = %q, want a .local/state/diktat path", got)
	}
}

func TestRuntimeDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	got, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/run/user/1000/diktat"; got != want {
		t.Errorf("RuntimeDir() = %q, want %q", got, want)
	}
}

// Unset has to fail rather than fall back: the only fallback available is
// /tmp, which is the place these files exist to stay out of.
func TestRuntimeDirUnset(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got, err := RuntimeDir(); err == nil {
		t.Errorf("RuntimeDir() = %q with no XDG_RUNTIME_DIR, want an error", got)
	}
}

// The spec says to honour the variable, and somebody who has set it has said
// where their configuration goes. Reading somewhere else means their file is
// ignored in silence, since a missing config is not an error.
func TestConfigDirFollowsTheVariable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/somewhere/else")
	if got := ConfigDir(); got != "/somewhere/else/diktat" {
		t.Errorf("ConfigDir = %q, want /somewhere/else/diktat", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/nobody")
	if got := ConfigDir(); got != "/home/nobody/.config/diktat" {
		t.Errorf("ConfigDir = %q, want the ~/.config fallback", got)
	}
}
