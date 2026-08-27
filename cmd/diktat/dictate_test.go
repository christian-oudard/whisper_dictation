package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/christian-oudard/diktat/internal/config"
)

// captureLog runs f with the log redirected, and returns what it wrote.
func captureLog(t *testing.T, f func()) string {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer log.SetOutput(os.Stderr)
	f()
	return buf.String()
}

func emptyConfig() *config.Config { return &config.Config{} }

// fakeRecorder hands the daemon a capture without a sound card.
type fakeRecorder struct {
	samples  []int16
	rebuilds int
}

func (f *fakeRecorder) Start()         {}
func (f *fakeRecorder) Stop() []int16  { return f.samples }
func (f *fakeRecorder) Rebuild() error { f.rebuilds++; return nil }
func (f *fakeRecorder) Close()         {}

// A load can fail on the first model of a session, and then there is nothing
// to transcribe with. The dictation path used to dereference the model here,
// which took the daemon down; the unit restarted it into the same failed load,
// so every attempt to dictate crashed it again.
func TestDictatingWithNoModel(t *testing.T) {
	speech := make([]int16, 16000)
	for i := range speech {
		speech[i] = int16(1000 + i%50) // not silence, so nothing short-circuits
	}
	fake := &fakeRecorder{samples: speech}
	d := &daemon{recorder: fake, cfg: emptyConfig()}

	logged := captureLog(t, func() { d.stopRecording() })

	if !strings.Contains(logged, "No model") {
		t.Errorf("log was %q; want it to say there is no model", logged)
	}
	if !strings.Contains(logged, "diktat model") {
		t.Errorf("log was %q; want it to say how to choose one", logged)
	}
	if fake.rebuilds != 0 {
		t.Errorf("rebuilt the audio device %d times; the microphone is not what is missing", fake.rebuilds)
	}
}

// Nothing captured is not an error and must not reach the model: pressing the
// key twice quickly is an ordinary thing to do.
func TestDictatingWithNoAudio(t *testing.T) {
	d := &daemon{recorder: &fakeRecorder{}, cfg: emptyConfig()}
	logged := captureLog(t, func() { d.stopRecording() })
	if !strings.Contains(logged, "No audio") {
		t.Errorf("log was %q; want it to say there was no audio", logged)
	}
}

// A capture with not one bit set in it means the microphone is gone, which on
// a bluetooth headset lasts for the rest of the session. Rebuilding the device
// is what brings it back, and getting this wrong cost a whole session of
// dictations that typed nothing.
func TestDeadMicrophoneIsOnTheBar(t *testing.T) {
	dir := t.TempDir()
	statusPath = filepath.Join(dir, "status")
	activityPath = filepath.Join(dir, "activity")

	d := &daemon{recorder: &fakeRecorder{samples: make([]int16, 16000)}, cfg: emptyConfig()}
	captureLog(t, func() { d.stopRecording() })
	raw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ERR") {
		t.Errorf("the bar says %q after a press that captured nothing at all", raw)
	}

	// Saying nothing is not a failure. The key is pressed and released around
	// silence often enough that a light scolding somebody for it is noise.
	quiet := make([]int16, 16000)
	for i := range quiet {
		quiet[i] = int16(i%3 - 1)
	}
	d2 := &daemon{recorder: &fakeRecorder{samples: quiet}, cfg: emptyConfig()}
	captureLog(t, func() { d2.stopRecording() })
	if d2.failed {
		t.Error("a silent capture lit the bar")
	}
}

func TestDeadMicrophoneIsRebuilt(t *testing.T) {
	fake := &fakeRecorder{samples: make([]int16, 16000)} // bit-exact zero
	d := &daemon{recorder: fake, cfg: emptyConfig()}

	logged := captureLog(t, func() { d.stopRecording() })

	if fake.rebuilds != 1 {
		t.Errorf("rebuilt %d times, want once", fake.rebuilds)
	}
	if !strings.Contains(logged, "microphone") {
		t.Errorf("log was %q; want it to say the microphone is why", logged)
	}
}

// Quiet is not dead. A room with nobody talking in it still carries noise, and
// rebuilding the audio device every time somebody presses the key without
// speaking would cost seconds for nothing.
func TestQuietCaptureIsNotRebuilt(t *testing.T) {
	quiet := make([]int16, 16000)
	for i := range quiet {
		quiet[i] = int16(i%3) - 1 // barely above nothing, but not nothing
	}
	fake := &fakeRecorder{samples: quiet}
	d := &daemon{recorder: fake, cfg: emptyConfig()}

	logged := captureLog(t, func() { d.stopRecording() })

	if fake.rebuilds != 0 {
		t.Errorf("rebuilt the audio device %d times for a quiet capture", fake.rebuilds)
	}
	if !strings.Contains(logged, "silent") {
		t.Errorf("log was %q; want it to say the capture was silent", logged)
	}
}

// A microphone that is dead is worth repairing whether or not a model is
// loaded: the repair is what makes the next dictation possible once there is
// one.
func TestDeadMicrophoneIsRebuiltWithNoModel(t *testing.T) {
	fake := &fakeRecorder{samples: make([]int16, 16000)}
	d := &daemon{recorder: fake, cfg: emptyConfig()}
	captureLog(t, func() { d.stopRecording() })
	if fake.rebuilds != 1 {
		t.Errorf("rebuilt %d times with no model loaded, want once", fake.rebuilds)
	}
}

// The history holds every sentence ever dictated, which is why it is written
// 0600. That mode applies only when the open call creates the file: one that
// already exists keeps what it had, so a history created before this rule --
// or by anything else -- stayed readable by everyone on the machine.
func TestHistoryTightensAnOpenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &daemon{cfg: &config.Config{HistoryFile: config.HistoryFile(path)}}

	d.appendHistory("what was said")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("history is mode %o, want nothing for group or other", perm)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "what was said") {
		t.Errorf("history is %q; want the dictation appended", body)
	}
}

// A path a person wrote by hand is likely to start with ~, and nothing else
// here expands it. Another user's home is not ours to guess at, though: it
// must not become a directory of that name under this one.
func TestHistoryPathExpansion(t *testing.T) {
	t.Setenv("HOME", "/home/nobody")
	if got, err := historyPath("~/notes.jsonl"); err != nil || got != "/home/nobody/notes.jsonl" {
		t.Errorf("historyPath(~/notes.jsonl) = %q, %v", got, err)
	}
	if got, _ := historyPath("~someone/notes.jsonl"); got != "~someone/notes.jsonl" {
		t.Errorf("historyPath expanded another user's home to %q", got)
	}
	if got, _ := historyPath("/tmp/plain.jsonl"); got != "/tmp/plain.jsonl" {
		t.Errorf("historyPath changed an absolute path to %q", got)
	}
}

// A dictation that was transcribed and could not be typed is on the bar. With
// no typing tool installed that is every dictation, and the key looks like it
// does nothing: the words exist, they are just not where they were asked for.
func TestStatusShowsATypeFailure(t *testing.T) {
	dir := t.TempDir()
	statusPath = filepath.Join(dir, "status")
	activityPath = filepath.Join(dir, "activity")
	status := func() string {
		raw, err := os.ReadFile(statusPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	d := &daemon{recorder: &fakeRecorder{}, cfg: emptyConfig()}
	d.failed = true
	d.restoreStatus()
	if !strings.Contains(status(), "ERR") {
		t.Errorf("the bar says %q after a dictation that could not be typed", status())
	}

	// The next press is about the next dictation, so the light goes with it.
	d.startRecording()
	if !strings.Contains(status(), "REC") {
		t.Errorf("the bar says %q while recording", status())
	}
	d.mu.Lock()
	d.recording = false
	d.mu.Unlock()
	d.restoreStatus()
	if got := status(); got != "" {
		t.Errorf("the bar still says %q one dictation later", got)
	}
}

// Recording outranks it: the microphone being live is worth more than what
// happened last time, and a load in flight is worth more than either.
func TestRecordingOutranksTheError(t *testing.T) {
	dir := t.TempDir()
	statusPath = filepath.Join(dir, "status")
	activityPath = filepath.Join(dir, "activity")

	d := &daemon{recorder: &fakeRecorder{}, cfg: emptyConfig(), failed: true, recording: true}
	d.restoreStatus()
	raw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "REC") {
		t.Errorf("the bar says %q while recording after a failed type", raw)
	}
}

// A press with no model to transcribe with is a press that produced nothing,
// so it lights the bar the same way a failed type does. Before this the
// daemon exited at startup instead, and the unit restarted it every two
// seconds until the start limit gave up -- after which downloading a model
// changed nothing, because what would have loaded it was dead.
func TestNoModelIsOnTheBar(t *testing.T) {
	dir := t.TempDir()
	statusPath = filepath.Join(dir, "status")
	activityPath = filepath.Join(dir, "activity")

	speech := make([]int16, 16000)
	for i := range speech {
		speech[i] = int16(1000 + i%50)
	}
	d := &daemon{recorder: &fakeRecorder{samples: speech}, cfg: emptyConfig()}
	captureLog(t, func() { d.stopRecording() })

	raw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ERR") {
		t.Errorf("the bar says %q after a press with no model", raw)
	}
	if d.model != nil {
		t.Error("a press loaded a model by itself")
	}
}
