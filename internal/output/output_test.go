package output

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// fakeClipboard stands up wl-paste, wl-copy and wtype as shell scripts over a
// file, so the paste flow can be exercised without a compositor. wtypeBody is
// what the paste chord does, which is where a test says whether anything else
// touched the clipboard while the dictation was in flight.
//
// The scripts mirror the two conventions the real tools have that the code
// depends on: wl-copy appends a trailing newline to what it is given, and
// wl-paste --no-newline takes one off and fails on an empty clipboard.
func fakeClipboard(t *testing.T, start, wtypeBody string) (env []string, read func() string) {
	t.Helper()
	dir := t.TempDir()
	clip := filepath.Join(dir, "clipboard")
	if err := os.WriteFile(clip, []byte(start), 0600); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write("wl-paste", `[ -s "$CLIP" ] || exit 1
printf '%s' "$(cat "$CLIP")"`)
	// The leading -- is dropped, then either the remaining argument or stdin is
	// what gets copied.
	write("wl-copy", `shift
if [ $# -gt 0 ]; then printf '%s\n' "$*" > "$CLIP"; else cat > "$CLIP"; fi`)
	write("wtype", wtypeBody)

	// LookPath resolves against the process environment, not cmd.Env, so the
	// fakes have to be on both.
	path := dir + ":" + os.Getenv("PATH")
	t.Setenv("PATH", path)
	env = []string{"CLIP=" + clip, "PATH=" + path}
	return env, func() string {
		b, err := os.ReadFile(clip)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
}

// A compositor whose stream cannot be parsed must not cost the dictation: the
// input method is one of three mechanisms and the other two still work. Three
// dictations in a row were lost to exactly this before the fallback existed,
// each to a registry frame the parser refused.
func TestTypeFallsBackWhenTheInputMethodFails(t *testing.T) {
	dir := t.TempDir()

	// As much of a compositor as the failure needs: accept, then answer the
	// handshake with a registry global event carrying a name and nothing
	// else -- no interface, no version.
	sock := filepath.Join(dir, "wayland-broken")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		frame := make([]byte, 12)
		binary.LittleEndian.PutUint32(frame[0:], 2)        // the registry
		binary.LittleEndian.PutUint32(frame[4:], 12<<16|0) // size 12, opcode global
		binary.LittleEndian.PutUint32(frame[8:], 33)       // a name
		c.Write(frame)
		io.Copy(io.Discard, c) // hold the connection until the client gives up
	}()

	// A wtype that records what it was asked to type.
	typed := filepath.Join(dir, "typed")
	script := "#!/bin/sh\n[ \"$1\" = -- ] && shift\nprintf '%s' \"$*\" > " + typed + "\n"
	if err := os.WriteFile(filepath.Join(dir, "wtype"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	// Both set, so waylandEnv goes no further than the environment. The
	// display is an absolute path, which the variable is allowed to hold.
	t.Setenv("SWAYSOCK", "inherited")
	t.Setenv("WAYLAND_DISPLAY", sock)

	if err := Type("the dictation", nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(typed)
	if err != nil {
		t.Fatalf("wtype never ran: %v", err)
	}
	if string(b) != "the dictation" {
		t.Errorf("typed %q", b)
	}
}

// The ordinary case: what was on the clipboard before the dictation is on it
// afterwards.
func TestPasteRestoresClipboard(t *testing.T) {
	env, clipboard := fakeClipboard(t, "notes for later\n", "exit 0")
	if err := paste(env, "the dictation ", []string{"-M", "ctrl", "v"}); err != nil {
		t.Fatal(err)
	}
	if got := clipboard(); got != "notes for later" {
		t.Errorf("clipboard = %q, want the saved text back", got)
	}
}

// Someone copied something between the dictation going on the clipboard and
// the restore. That copy is newer than the one being put back, so it stands.
func TestPasteLeavesANewerCopyAlone(t *testing.T) {
	env, clipboard := fakeClipboard(t, "notes for later\n", `printf 'copied mid-dictation\n' > "$CLIP"`)
	if err := paste(env, "the dictation ", []string{"-M", "ctrl", "v"}); err != nil {
		t.Fatal(err)
	}
	if got := clipboard(); got != "copied mid-dictation\n" {
		t.Errorf("clipboard = %q, want the newer copy left alone", got)
	}
}

// An empty clipboard is what wl-paste fails on, and failing to read it must
// not turn into failing to restore: the dictation would be left on it.
func TestPasteRestoresOverAnEmptyClipboard(t *testing.T) {
	env, clipboard := fakeClipboard(t, "", "exit 0")
	if err := paste(env, "the dictation ", []string{"-M", "ctrl", "v"}); err != nil {
		t.Fatal(err)
	}
	if got := clipboard(); got != "" {
		t.Errorf("clipboard = %q, want it empty again", got)
	}
}
