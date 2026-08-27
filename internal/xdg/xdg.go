// Package xdg names diktat's directories under the XDG base directory spec.
//
// It is a leaf on purpose. The two packages that need a directory are config,
// which is where a menu choice is remembered, and ipc, which is where the
// commands find each other; having ipc ask config for the path made a package
// of four files depend on the one that downloads models, and so on the http
// stack, which is a lot of build to name a directory.
package xdg

import (
	"errors"
	"os"
	"path/filepath"
)

// ConfigDir is where the hand-authored config lives: $XDG_CONFIG_HOME/diktat,
// or ~/.config/diktat. The spec says to honour the variable, and a person who
// has set it has said where their configuration goes; reading somewhere else
// means their file is ignored with no message, since a missing config is not
// an error.
func ConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "diktat")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "diktat")
}

// StateDir is where what diktat decided lives, and it outlives the session:
// $XDG_STATE_HOME/diktat, or ~/.local/state/diktat. Deleting it costs nothing
// that cannot be decided again.
func StateDir() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "diktat")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "diktat")
}

// RuntimeDir is where what this session is doing lives: $XDG_RUNTIME_DIR is
// per-user, mode 0700, and emptied when the user's last session ends, which
// is exactly the lifetime of the files it holds.
//
// Unset is an error rather than a fallback to /tmp. This is a Wayland
// dictation tool, the compositor's own socket lives in that directory, and
// wtype would have nothing to type into without it; falling back would mean
// quietly writing the private files to the public place in the one case the
// variable is missing.
func RuntimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		return "", errors.New("XDG_RUNTIME_DIR is not set")
	}
	return filepath.Join(base, "diktat"), nil
}
