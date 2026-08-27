package output

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// socket makes a file named the way sway names its IPC socket. A plain file is
// enough: nothing here connects, it only reads the PID out of the name.
func socket(t *testing.T, dir string, pid int) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("sway-ipc.%d.%d.sock", os.Getuid(), pid))
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// deadPID returns a PID that has run and been reaped, which is how a leftover
// socket from an ended session looks.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestSwaySocketSkipsLeftovers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	socket(t, dir, deadPID(t))
	want := socket(t, dir, os.Getpid())

	got, pid, err := swaySocket()
	if err != nil {
		t.Fatal(err)
	}
	if got != want || pid != os.Getpid() {
		t.Errorf("swaySocket() = %q, %d, want %q, %d", got, pid, want, os.Getpid())
	}
}

// Picking between two live compositors would mean typing into whichever was
// guessed, so it has to fail instead.
func TestSwaySocketAmbiguous(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	socket(t, dir, os.Getpid())
	socket(t, dir, os.Getppid())

	if _, _, err := swaySocket(); err == nil {
		t.Error("swaySocket() with two live sockets = nil error, want an error")
	}
}

func TestSwaySocketNoneLive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	socket(t, dir, deadPID(t))

	if _, _, err := swaySocket(); err == nil {
		t.Error("swaySocket() with only a leftover = nil error, want an error")
	}
}

func TestSwaySocketNoRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	if _, _, err := swaySocket(); err == nil {
		t.Error("swaySocket() with no XDG_RUNTIME_DIR = nil error, want an error")
	}
}

// An inherited value is used as it stands, and nothing is looked up: this is
// the path a keybinding takes, where there is no runtime directory to search
// and no reason to search one.
func TestWaylandEnvKeepsInherited(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("SWAYSOCK", "/run/sway.sock")
	t.Setenv("WAYLAND_DISPLAY", "wayland-9")

	env, display, err := waylandEnv()
	if err != nil {
		t.Fatal(err)
	}
	if display != "wayland-9" {
		t.Errorf("display = %q, want the inherited value", display)
	}
	for _, want := range []string{"SWAYSOCK=/run/sway.sock", "WAYLAND_DISPLAY=wayland-9"} {
		if count(env, want) != 1 {
			t.Errorf("waylandEnv() has %d of %q, want 1", count(env, want), want)
		}
	}
}

func TestWaylandEnvFindsSocket(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("SWAYSOCK", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-9")
	want := "SWAYSOCK=" + socket(t, dir, os.Getpid())

	env, display, err := waylandEnv()
	if err != nil {
		t.Fatal(err)
	}
	if count(env, want) != 1 {
		t.Errorf("waylandEnv() has %d of %q, want 1", count(env, want), want)
	}
	// Returned beside the environment as well as in it, since the input method
	// connects from this process rather than spawning something that reads the
	// variable.
	if display != "wayland-9" {
		t.Errorf("display = %q, want wayland-9", display)
	}
}

func count(env []string, entry string) int {
	n := 0
	for _, e := range env {
		if e == entry {
			n++
		}
	}
	return n
}

func TestLookupEnviron(t *testing.T) {
	raw := []byte("PATH=/bin\x00WAYLAND_DISPLAY=wayland-1\x00XDG_SESSION_TYPE=wayland\x00")

	got, err := lookupEnviron(raw, "WAYLAND_DISPLAY")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wayland-1" {
		t.Errorf("lookupEnviron() = %q, want %q", got, "wayland-1")
	}
	if _, err := lookupEnviron(raw, "SWAYSOCK"); err == nil {
		t.Error("lookupEnviron() for a missing name = nil error, want an error")
	}
}

// The daemon reads this out of the compositor, so it has to work against a
// live process rather than only against a fixture. Our own PID is the one
// process guaranteed to be running, and its initial environment holds PATH.
func TestProcEnv(t *testing.T) {
	got, err := procEnv(os.Getpid(), "PATH")
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Error("procEnv() = empty, want this process's PATH")
	}
	if _, err := procEnv(deadPID(t), "PATH"); err == nil {
		t.Error("procEnv() on a dead PID = nil error, want an error")
	}
}
