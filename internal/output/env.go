// Finding the compositor. Everything this package spawns talks to Wayland or
// to sway, so each one needs WAYLAND_DISPLAY and SWAYSOCK, and the daemon is
// a systemd user service: it starts before any compositor exists and inherits
// nothing of one. The usual fix is for the compositor's config to push the
// variables in with `systemctl --user import-environment`, which makes the
// session responsible for starting the daemon and copies the values once.
// Copying once is the worse half: SWAYSOCK carries sway's PID, so a
// compositor restart leaves the daemon holding a name for a socket nobody is
// listening on, and dictation types into nothing until the daemon is
// restarted too.
//
// So look them up instead, per child process, when there is a child to spawn.
// By then someone has pressed a key, which means a compositor is running.
// XDG_RUNTIME_DIR is set by pam_systemd before any of this and holds both
// sockets; the IPC socket names its PID, which is what separates the live
// compositor from what an earlier session left behind.
package output

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// waylandEnv returns the environment for a child process, filling in
// WAYLAND_DISPLAY and SWAYSOCK when they are not already set. An inherited
// value wins: a `diktat repeat` bound to a key runs inside the session and
// already knows, and so does a daemon started by hand from a terminal.
//
// The display is returned beside the environment as well as in it, since the
// input method connects to the compositor from this process rather than
// spawning something that reads the variable.
func waylandEnv() ([]string, string, error) {
	env := os.Environ()
	sock, display := os.Getenv("SWAYSOCK"), os.Getenv("WAYLAND_DISPLAY")
	if sock != "" && display != "" {
		return env, display, nil
	}
	path, pid, err := swaySocket()
	if err != nil {
		return nil, "", err
	}
	if sock == "" {
		env = append(env, "SWAYSOCK="+path)
	}
	if display == "" {
		// Read it off the compositor rather than guessing at the wayland-N
		// sockets in the same directory: a greeter that has exited, a nested
		// session and an Xwayland all leave names there, and the one that
		// matches the socket just picked is the one sway itself is using.
		display, err = procEnv(pid, "WAYLAND_DISPLAY")
		if err != nil {
			return nil, "", err
		}
		env = append(env, "WAYLAND_DISPLAY="+display)
	}
	return env, display, nil
}

// swaySocket returns the live sway IPC socket and the PID of the compositor
// holding it. sway names it sway-ipc.<uid>.<pid>.sock, so signal 0 against
// that PID says whether the file is a session or a leftover.
//
// Two live compositors is an error rather than a choice. Either one is a
// plausible target and the wrong one types someone's dictation into the wrong
// screen, so say so instead of picking.
func swaySocket() (string, int, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return "", 0, errors.New("XDG_RUNTIME_DIR is not set")
	}
	prefix := fmt.Sprintf("sway-ipc.%d.", os.Getuid())
	matches, err := filepath.Glob(filepath.Join(dir, prefix+"*.sock"))
	if err != nil {
		return "", 0, err
	}
	var live []string
	var pid int
	for _, path := range matches {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), prefix), ".sock")
		n, err := strconv.Atoi(name)
		if err != nil || syscall.Kill(n, 0) != nil {
			continue
		}
		live = append(live, path)
		pid = n
	}
	switch len(live) {
	case 0:
		return "", 0, fmt.Errorf("no live sway socket in %s", dir)
	case 1:
		return live[0], pid, nil
	default:
		return "", 0, fmt.Errorf("several live sway sockets: %s", strings.Join(live, " "))
	}
}

// procEnv reads one variable out of a running process's environment.
func procEnv(pid int, name string) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return "", err
	}
	return lookupEnviron(raw, name)
}

// lookupEnviron finds name in NUL-separated KEY=VALUE data, the format
// /proc/<pid>/environ is in.
func lookupEnviron(raw []byte, name string) (string, error) {
	for _, entry := range bytes.Split(raw, []byte{0}) {
		key, value, ok := bytes.Cut(entry, []byte{'='})
		if ok && string(key) == name {
			return string(value), nil
		}
	}
	return "", fmt.Errorf("%s is not in the compositor's environment", name)
}
