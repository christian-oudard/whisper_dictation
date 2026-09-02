// Package output types transcribed text via wtype, or pastes via the
// clipboard for apps where wtype is too slow. Speed is the whole reason the
// clipboard is involved at all: pasting touches state outside the target
// window and typing does not, so nothing else would justify it.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/christian-oudard/diktat/internal/wayland"
)

// pasteChords maps a config-level typing-method label to the wtype argv that
// performs that key chord.
var pasteChords = map[string][]string{
	"C-v":   {"-M", "ctrl", "v", "-m", "ctrl"},
	"C-S-v": {"-M", "ctrl", "-M", "shift", "v", "-m", "shift", "-m", "ctrl"},
}

// Type sends text to the focused Wayland window, by whichever of three
// mechanisms the machine in front of it can manage.
//
// The compositor's input method is tried first and is the only one that is
// not pretending to be a keyboard: it carries the whole string in one message,
// inserted through the application's own text input path. It is unavailable
// more often than not -- the protocol is a wlroots one, another input method
// may hold the seat, and the focused window may have nowhere to put text --
// and each of those is an ordinary state rather than a fault, so it falls
// through rather than failing.
//
// wtype is the fallback, and it touches nothing outside the target window. It
// synthesises a key press and release per character, and an app that runs its
// full input handling on each one takes long enough over a dictation to be
// worth working around, which is what typingMethods is for: an app_id listed
// there gets a clipboard paste instead.
//
// The table is consulted only after the input method declines, since an entry
// in it describes a slow keystroke path that the input method does not use.
func Type(text string, typingMethods map[string]string) error {
	env, display, err := waylandEnv()
	if err != nil {
		return err
	}
	if err := wayland.Insert(display, text); err == nil {
		return nil
	} else if !wayland.Unavailable(err) {
		// The mechanism was there and failed anyway. The dictation still has
		// a keyboard path, so the text goes on screen; the journal carries
		// the failure, because a broken input method must stay visible.
		// Failing the dictation to advertise it was tried, and cost three
		// dictations in a row to a parse error the fallback typed fine.
		log.Printf("input method: %v; typing instead", err)
	}
	appID, _ := focusedAppID(env)
	method, ok := typingMethods[appID]
	if ok {
		args, known := pasteChords[method]
		if !known {
			return fmt.Errorf("unknown typing method %q for app_id %q", method, appID)
		}
		return paste(env, text, args)
	}
	return command(env, "wtype", "--", text).Run()
}

// command is exec.Command with the compositor's environment, which every
// program spawned here needs and none of them can be given by systemd.
func command(env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	return cmd
}

// paste saves the clipboard, sets it to text, sends the paste chord, and puts
// back what was there.
//
// Putting it back is conditional, because nothing sequences these programs
// against the application being pasted into. wtype returns when the keystroke
// is injected, not when the target has acted on it, so the restore races the
// target's read of the clipboard. It wins in practice only because it is a
// fresh process, and forking, linking and connecting to the compositor costs
// more than the target needs to ask for the data. That is an accident of
// process startup rather than a guarantee, and no guarantee is available from
// these tools: knowing that a read happened means owning the selection in this
// process, over wlr-data-control, rather than shelling out for it.
//
// What can be settled without any timing assumption is who owns the clipboard
// now. If it no longer holds the dictation, something else took it, most
// likely the user copying something while this was in flight, and their copy
// outranks the one being put back.
func paste(env []string, text string, chord []string) error {
	saved, _ := command(env, "wl-paste", "--no-newline").Output()
	if err := command(env, "wl-copy", "--", text).Run(); err != nil {
		return fmt.Errorf("wl-copy: %w", err)
	}
	_ = command(env, "wtype", chord...).Run()
	if !holds(env, text) {
		return nil
	}
	restore := command(env, "wl-copy", "--")
	restore.Stdin = bytes.NewReader(saved)
	return restore.Run()
}

// holds reports whether the clipboard still carries text.
//
// Trailing newlines are ignored on both sides. wl-copy appends one to what it
// is given and wl-paste --no-newline takes one off, so the two should cancel,
// and a mismatch in that bookkeeping would not look like a mismatch: it would
// silently stop the clipboard ever being restored, which is the failure this
// whole function exists to avoid.
//
// An unreadable clipboard answers no. wl-paste fails on an empty one, and
// empty is not what was put there either.
func holds(env []string, text string) bool {
	now, err := command(env, "wl-paste", "--no-newline").Output()
	if err != nil {
		return false
	}
	return strings.TrimRight(string(now), "\n") == strings.TrimRight(text, "\n")
}

// focusedAppID returns the sway app_id of the focused window.
func focusedAppID(env []string) (string, error) {
	out, err := command(env, "swaymsg", "-t", "get_tree").Output()
	if err != nil {
		return "", err
	}
	var tree json.RawMessage
	if err := json.Unmarshal(out, &tree); err != nil {
		return "", err
	}
	return findFocused(tree), nil
}

func findFocused(raw json.RawMessage) string {
	var node struct {
		Focused       bool              `json:"focused"`
		AppID         *string           `json:"app_id"`
		Nodes         []json.RawMessage `json:"nodes"`
		FloatingNodes []json.RawMessage `json:"floating_nodes"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		return ""
	}
	if node.Focused && node.AppID != nil {
		return *node.AppID
	}
	for _, child := range node.Nodes {
		if id := findFocused(child); id != "" {
			return id
		}
	}
	for _, child := range node.FloatingNodes {
		if id := findFocused(child); id != "" {
			return id
		}
	}
	return ""
}
