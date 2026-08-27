package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// history_file takes a path, or says yes or no. A bool that failed to parse
// took the whole file with it, and with it a typing_methods table that had
// been working: every dictation went back to being typed a character at a
// time, and the only word about it was a line in a log that was truncated at
// every start.
func TestHistoryFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")

	for _, c := range []struct {
		name string
		toml string
		want string
	}{
		{"a path", `history_file = "~/dictation.jsonl"`, "~/dictation.jsonl"},
		{"false", `history_file = false`, ""},
		{"absent", ``, ""},
		{"true", `history_file = true`, "/state/diktat/history.jsonl"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, _, err := Load(write(t, c.toml))
			if err != nil {
				t.Fatal(err)
			}
			if got := string(cfg.HistoryFile); got != c.want {
				t.Errorf("HistoryFile = %q, want %q", got, c.want)
			}
		})
	}
}

// The rest of the file has to survive whatever history_file says, since one
// key going wrong used to mean every other setting silently reverting to its
// default.
func TestOneKeyDoesNotTakeTheFileWithIt(t *testing.T) {
	cfg, unknown, err := Load(write(t, `history_file = false

[typing_methods]
foot = "C-S-v"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 0 {
		t.Errorf("unknown keys %v, want none", unknown)
	}
	if got := cfg.TypingMethods["foot"]; got != "C-S-v" {
		t.Errorf("TypingMethods[foot] = %q, want %q", got, "C-S-v")
	}
}

// Anything else is a mistake worth reporting rather than a value to guess at.
func TestHistoryFileRejectsOtherTypes(t *testing.T) {
	if _, _, err := Load(write(t, `history_file = 3`)); err == nil {
		t.Error("history_file = 3 loaded without error")
	}
}
