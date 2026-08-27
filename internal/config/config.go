// Package config reads ~/.config/diktat/config.toml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/christian-oudard/diktat/internal/xdg"
)

// Config is the on-disk schema. All fields are optional.
type Config struct {
	// Model the daemon starts on. `diktat model` switches the running daemon
	// without touching this, so a restart comes back to a known model rather
	// than to whatever was last selected.
	Model string `toml:"model"`
	// TypingMethods names how to get text into an app that is slow to accept
	// it a keystroke at a time. Keyed by sway app_id; every value today is a
	// paste chord, since pasting is the only alternative there is so far.
	TypingMethods map[string]string `toml:"typing_methods"`
	HistoryFile   HistoryFile       `toml:"history_file"`
}

// HistoryFile is where each transcription is appended, or empty for nowhere.
//
// It reads a path or a bool. false is the same as leaving the key out, no
// history; true keeps one at DefaultHistoryPath, so wanting a history costs
// nobody a decision about where to put it. Both halves of that matter: on or
// off is what a person reaches for first, and a key that takes only a path
// answers `false` by failing to parse the whole file, taking every other
// setting down with it. That is how a working typing_methods table went
// quietly missing and every dictation went back to being typed one character
// at a time.
type HistoryFile string

// DefaultHistoryPath is where `history_file = true` writes. XDG names state
// as the place for "actions history", and it already holds the model choice.
func DefaultHistoryPath() string {
	return filepath.Join(xdg.StateDir(), "history.jsonl")
}

func (h *HistoryFile) UnmarshalTOML(v any) error {
	switch value := v.(type) {
	case string:
		*h = HistoryFile(value)
		return nil
	case bool:
		if value {
			*h = HistoryFile(DefaultHistoryPath())
		} else {
			*h = ""
		}
		return nil
	}
	return fmt.Errorf("history_file must be a path, true or false, not %T", v)
}

// DefaultPath returns the standard config location, honouring
// XDG_CONFIG_HOME the way the state directory honours XDG_STATE_HOME.
func DefaultPath() string {
	return filepath.Join(xdg.ConfigDir(), "config.toml")
}

// Load parses the config file at path. A missing file returns a zero Config
// and no error, so callers can ignore the absence of user config.
//
// Unknown keys come back in the second return. TOML ignores them silently,
// which is how a key that had stopped meaning anything sat in a real config
// nothing: it was never in this struct, so it was never read, and nothing
// ever said so. A typo deserves the same treatment.
func Load(path string) (*Config, []string, error) {
	var c Config
	meta, err := toml.DecodeFile(path, &c)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &c, nil, nil
		}
		return nil, nil, err
	}
	var unknown []string
	for _, key := range meta.Undecoded() {
		unknown = append(unknown, key.String())
	}
	return &c, unknown, nil
}
