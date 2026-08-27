package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/christian-oudard/diktat/internal/models"
	"github.com/christian-oudard/diktat/internal/xdg"
)

// State is what diktat decided, as opposed to what the user wrote. The config
// file is hand-authored, so nothing here rewrites it: losing someone's
// comments and ordering to record a menu choice would be rude, and a file
// that is sometimes yours and sometimes the program's is worse than two
// files.
//
// XDG puts this in XDG_STATE_HOME, "state data that should persist between
// restarts ... that can be reused on a restart". Deleting it costs nothing:
// the daemon falls back to the config file, and then to the built-in default.

// selectedPath holds the model chosen by the last `diktat model`.
func selectedPath() string { return filepath.Join(xdg.StateDir(), "model") }

// Selected is the model the user last chose, or "" if they never have. It is
// a menu name or a path, never a menu number, so it survives the menu being
// reordered.
func Selected() string {
	raw, err := os.ReadFile(selectedPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// StartModel is the model a daemon started now would load: the last choice,
// then the configured one, then the built-in default. The choice outranks the
// config because it is the more recent instruction from the same person, and
// it is cleared by deleting one file. The menu asks this too, so that it can
// mark the model in use when no daemon is running to have loaded one.
func StartModel() string {
	if name := Selected(); name != "" {
		return name
	}
	cfg, _, err := Load(DefaultPath())
	if err == nil && cfg.Model != "" {
		return cfg.Model
	}
	return models.Default
}

// txPath holds the pipeline chosen by the last `diktat tx-model`. A second
// file rather than a second line in the first: the two choices are unrelated,
// nothing reads both, and one that outlives its menu entry should not take the
// other down with it.
func txPath() string { return filepath.Join(xdg.StateDir(), "tx-model") }

// TxModel is the pipeline chosen for transcribing recordings, or the default
// when nothing has been chosen. There is no config-file key behind this the
// way there is for the dictation model: that one is read by a daemon that
// starts before anyone can choose, and this one by a command someone ran.
func TxModel() string {
	raw, err := os.ReadFile(txPath())
	if err != nil {
		return models.DefaultDiarizer
	}
	return strings.TrimSpace(string(raw))
}

// SelectTx records a pipeline as the one to transcribe recordings with.
func SelectTx(name string) error {
	if err := os.MkdirAll(xdg.StateDir(), 0755); err != nil {
		return err
	}
	return os.WriteFile(txPath(), []byte(name+"\n"), 0644)
}

// Select records a model as the one to start with from now on. A failure to
// write is not worth failing the switch over: the model still changes, it
// just will not be remembered, so callers report it and carry on.
func Select(nameOrPath string) error {
	if err := os.MkdirAll(xdg.StateDir(), 0755); err != nil {
		return err
	}
	return os.WriteFile(selectedPath(), []byte(nameOrPath+"\n"), 0644)
}
