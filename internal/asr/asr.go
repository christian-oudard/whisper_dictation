// Package asr transcribes audio with transcribe.cpp, which covers whisper,
// moonshine and the other families behind one API. The model is a GGUF file;
// which architecture it is comes from the file, not from us.
package asr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	transcribe "github.com/handy-computer/transcribe.cpp/bindings/go"
)

// quiet takes the library's narration off stderr, which is where it goes
// otherwise: it describes every model load in detail, and the daemon has its
// own log while the offline tools print results. Once per process, since the
// sink is global.
//
// Dropped is not the same as silenced, though. What it says when a load fails
// is the only description of why there is, and the error the API returns is
// four words: "canary-qwen-2.5b-Q5_K_M.gguf: gguf load error" is not something
// anyone can act on. So the complaints are kept, and a failed load quotes the
// ones it caused.
var quiet sync.Once

// complaints is the library's last few warnings and errors, oldest first.
// Bounded because nothing empties it: a run that warns on every utterance
// would otherwise grow it for the life of the daemon.
var complaints struct {
	sync.Mutex
	lines []string
	// seen counts every line ever kept, which lines cannot: a mark taken
	// against its length would still be in range after the ring had rolled
	// past it, and the lines it was taken to exclude would read as included.
	seen int
}

const keptComplaints = 8

func keepComplaints() {
	transcribe.SetLogHandler(func(level transcribe.LogLevel, msg string) {
		if level != transcribe.LogWarn && level != transcribe.LogError {
			return
		}
		noteComplaint(msg)
	})
}

func noteComplaint(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	// Messages arrive on whichever thread produced them, including from
	// inside a run, so this is not the main loop's alone.
	complaints.Lock()
	defer complaints.Unlock()
	complaints.lines = append(complaints.lines, msg)
	complaints.seen++
	if len(complaints.lines) > keptComplaints {
		complaints.lines = complaints.lines[len(complaints.lines)-keptComplaints:]
	}
}

// complaintMark is where the log stands now, to read what an operation
// complained about back from afterwards.
func complaintMark() int {
	complaints.Lock()
	defer complaints.Unlock()
	return complaints.seen
}

// since renders what was complained about after mark, for the end of an error
// message. Kept to what the ring still holds, so a very noisy operation
// reports its last few rather than nothing.
func since(mark int) string {
	complaints.Lock()
	defer complaints.Unlock()
	n := min(complaints.seen-mark, len(complaints.lines))
	if n <= 0 {
		return ""
	}
	return ": " + strings.Join(complaints.lines[len(complaints.lines)-n:], "; ")
}

// Model is a loaded recognizer. It holds the weights and the decoder state,
// so it is worth keeping resident, and it is single-threaded.
type Model struct {
	s    *transcribe.Session
	name string
	// gpu is the device it landed on, or "" for CPU, and device is its index
	// among the registered devices, which is what the runtime's per-device
	// queries take.
	gpu    string
	device int
	// resident is what the weights and the context cost on the device, and
	// graph is what the compute buffers have grown to on top of them. longest
	// is the longest clip run so far, which is the length graph was measured
	// at: ggml keeps those buffers at their high-water mark, so the pair says
	// both what this model costs a cache now and what another second of audio
	// would add.
	resident uint64
	graph    uint64
	longest  time.Duration
	// timings is where the last transcription went, and load is where the
	// load itself went.
	timings Timings
	load    LoadTimings
	// mu guards the three above. A run is single-threaded, but it is not
	// always on the thread that asks what the model costs: the daemon
	// rehearses in the background and its main loop goes on reporting the
	// cache size meanwhile.
	mu sync.Mutex
}

// sampleRate is the rate every model here is fed at. Named rather than
// imported: internal/audio pulls in the capture library, and this package is
// linked into tools that never record.
const sampleRate = 16000

// Timings is where a transcription's time went. Encode dominates for whisper,
// which runs it over a padded 30 second window however long the utterance.
//
// Other is the run's wall time less the three the library accounts for. It is
// normally noise, and is not noise on a model whose graph the backend has not
// compiled yet: that cost lands between the stages rather than inside one, so
// without this the log shows a fast transcription that took five seconds.
type Timings struct {
	Mel, Encode, Decode, Other time.Duration
}

// LoadTimings is where a load's time went, split at the only seam visible
// from here: pulling the file off disk, and everything the library does with
// it afterwards.
//
// Worth splitting because a load is otherwise a single opaque number, and one
// that ran long has no shortage of candidate explanations: a cold page cache,
// a busy disk, the weights going over PCIe, the backend allocating. A
// two-minute load of a 523 MiB model was blamed on three different things
// before anyone could say which half it was even in.
type LoadTimings struct {
	Read, Open time.Duration
}

func (t LoadTimings) String() string {
	return fmt.Sprintf("read %s, open %s",
		t.Read.Round(time.Millisecond), t.Open.Round(time.Millisecond))
}

// LoadTimings is where this model's load went.
func (m *Model) LoadTimings() LoadTimings { return m.load }

// Load opens a GGUF model and keeps it open.
func Load(path string) (*Model, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("model: %w", err)
	}
	quiet.Do(keepComplaints)

	opts, gpu, err := placement()
	if err != nil {
		return nil, err
	}
	// Read the file through once before handing it over. This is a
	// measurement first: the library reads it again immediately, from the
	// page cache this read just filled, so the two numbers below separate
	// waiting for a disk from everything else. It is close to free when the
	// cache is already warm, and when it is not, the read happens either way
	// and this is only the half of it that can be timed.
	read := time.Now()
	if err := prefetch(path); err != nil {
		return nil, fmt.Errorf("model: %w", err)
	}
	timings := LoadTimings{Read: time.Since(read)}

	before := deviceFree(opts)
	mark := complaintMark()
	opened := time.Now()
	s, err := transcribe.Open(path, opts, nil)
	timings.Open = time.Since(opened)
	if err != nil {
		// With what the library said about it. "gguf load error" on its own
		// says a file did not load and nothing about which part of it the
		// library could not read, which is the whole question.
		return nil, fmt.Errorf("%s: %w%s", filepath.Base(path), err, since(mark))
	}
	m := &Model{
		s:        s,
		name:     strings.TrimSuffix(filepath.Base(path), ".gguf"),
		gpu:      gpu,
		device:   deviceIndex(opts),
		resident: uint64(info.Size()),
		load:     timings,
	}
	// What the load itself took off the device, which is more than the file:
	// the weights are joined by the context the session keeps. A backend that
	// reports no memory leaves the file size standing, which is a floor rather
	// than an estimate.
	if after := deviceFree(opts); before > after {
		m.resident = before - after
	}
	return m, nil
}

// prefetch reads a file and keeps none of it, so that what it cost to pull off
// disk is a number of its own rather than part of the load's.
func prefetch(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(io.Discard, f)
	return err
}

// Bytes is what this model costs on its device now: the weights and context it
// loaded with, plus the compute buffers its transcriptions have grown to.
//
// The second half is not a detail. ggml allocates compute buffers on the first
// graph run of a shape and keeps them at the high-water mark, and for every
// family here except whisper that mark grows with the length of the audio. One
// four minute dictation took canary-180m-flash, a 151 MiB model, to 4.3 GiB.
func (m *Model) Bytes() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resident + m.graph
}

// AudioLimit is the longest clip to hand this model in one run: the shorter of
// what it says it accepts and what the device can still afford. 0 means
// neither imposes one.
//
// The model's own figure is not enough on its own. Every family here except
// whisper encodes the whole clip in one graph and whisper windows internally,
// so for the rest the activations grow with the length until the card cannot
// hold them, and the Vulkan backend does not survive that: the allocation
// fails and the process dies with it. Measured here, granite died on three
// minutes of audio against the six and a half it advertises, and
// canary-180m-flash, which advertises no limit at all, was within a minute of
// the same fate at five.
func (m *Model) AudioLimit() time.Duration {
	return shorter(m.MaxAudio(), m.fits())
}

// shorter is the tighter of two limits, where 0 on either side means that side
// imposes none. Split out so the arithmetic can be tested without a model.
func shorter(a, b time.Duration) time.Duration {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	case a < b:
		return a
	}
	return b
}

// fits is the longest clip whose graph the device can still afford, or 0 when
// nothing has been measured to say.
//
// Anything up to the longest clip already run costs no new allocation, since
// the buffers for it are held, so that length is always safe and is the floor.
// Past it, a quarter of the device's free memory is what may be spent at the
// rate a second of audio has been costing.
//
// A quarter rather than all of it because this extrapolates past every length
// measured, and against four families the slope taken from short clips
// understates the true one by up to half again. The floor makes the estimate
// self-correcting: every clip that runs re-measures at its own length, so the
// limit climbs with use. Being wrong the safe way costs a seam in a very long
// dictation, and being wrong the other way costs the daemon.
func (m *Model) fits() time.Duration {
	free := m.free()
	if free == 0 {
		// The CPU, or a backend that reports no memory. Neither can be
		// measured against, and neither is the one that dies.
		return 0
	}
	m.mu.Lock()
	graph, longest := m.graph, m.longest
	m.mu.Unlock()
	if fit := fitsIn(free/4, graph, longest); fit > 0 {
		return fit
	}
	return unmeasuredLimit
}

// unmeasuredLimit is the longest clip to hand a model that has run nothing
// yet, and so has no rate to be bounded by. Half a minute is what the warmup
// rehearses to and what whisper windows to internally, so every model here
// takes it comfortably, and running it is what supplies the measurement the
// clip after it is bounded by.
//
// The daemon warms before it serves and never meets this. It is here for a
// caller that does not, where the cost of it is one extra seam in a long clip
// and the cost of its absence is a dead process.
const unmeasuredLimit = 30 * time.Second

// fitsIn is how long a clip can get when spare bytes are left to spend and
// graph bytes have already bought longest of audio. Split from fits so the
// policy can be tested without a device.
func fitsIn(spare, graph uint64, longest time.Duration) time.Duration {
	if graph == 0 || longest == 0 {
		return 0
	}
	// In floating point because the integer form overflows: a duration is
	// nanoseconds, and a gigabyte of spare memory times half a minute of them
	// is past what an int64 holds. Nothing here needs the precision anyway,
	// since the rate itself is an estimate.
	return longest + time.Duration(float64(longest)*float64(spare)/float64(graph))
}

// free is the device's free memory, or 0 on the CPU, which has enough of it
// that no clip here is worth bounding against.
func (m *Model) free() uint64 {
	if !m.OnGPU() {
		return 0
	}
	devices, err := transcribe.Devices()
	if err != nil || m.device < 0 || m.device >= len(devices) {
		return 0
	}
	return devices[m.device].MemoryFree
}

// ErrTruncated means the decode hit the model's output budget before it
// finished. The transcript that comes back is real but incomplete.
//
// An audio-LLM reaches it on input with no speech in it: given noise it has
// nothing to transcribe and generates until the budget runs out. That makes
// it the expected answer to a warmup, and a real problem for an utterance.
var ErrTruncated = transcribe.ErrOutputTruncated

// ErrAborted means the run gave up because its context was cancelled. The
// caller asked for that, so it is not a failure of the model: a rehearsal
// that meets it is one to run again later, not one to give up on.
var ErrAborted = transcribe.ErrAborted

// Timings is where the last transcription's time went.
func (m *Model) Timings() Timings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.timings
}

// MaxAudio is the longest clip this model will take in one run, or 0 when it
// has no practical limit because the family chunks internally. It is the
// session's effective figure, so a context setting that lowers it is
// accounted for.
//
// This is what bounds a recording, rather than a number the daemon picks:
// whisper and parakeet chunk and answer 0, granite answers 6m24s.
func (m *Model) MaxAudio() time.Duration {
	limits, err := m.s.Limits()
	if err != nil {
		return 0
	}
	return limits.MaxAudio
}

// Languages are the language codes the model advertises accepting as a hint.
// Empty means it advertises no set, which is not the same as accepting none.
func (m *Model) Languages() ([]string, error) {
	c, err := m.s.Model().Capabilities()
	if err != nil {
		return nil, err
	}
	return c.Languages, nil
}

// MaxTimestamps is the finest alignment the model can produce. It decides
// whether a transcript can carry speaker labels: attribution is a join between
// words and speaker rows, so a model that stamps only whole segments cannot be
// joined to a diarizer however good its transcripts are.
func (m *Model) MaxTimestamps() transcribe.Timestamps {
	c, err := m.s.Model().Capabilities()
	if err != nil {
		return transcribe.StampsAuto
	}
	return c.MaxTimestamps
}

// Supports probes a behavioural toggle, such as whether the model attributes
// speech to speakers at all. Setting a run option the model does not implement
// is usually a warning rather than an error, so anything that depends on the
// difference asks first.
func (m *Model) Supports(f transcribe.Feature) bool { return m.s.Model().Supports(f) }

// AcceptsExtension probes for a family knob. Acceptance is per variant rather
// than per family, and passing one a model does not take fails the run, so a
// tool that sets one on a model it did not choose asks first.
func (m *Model) AcceptsExtension(slot transcribe.ExtSlot, kind transcribe.ExtKind) bool {
	return m.s.Model().AcceptsExtension(slot, kind)
}

// deviceIndex is which registered device a load with these options lands on.
// Zero means the first, which is what the library picks unaided.
func deviceIndex(opts *transcribe.LoadOptions) int {
	if opts == nil {
		return 0
	}
	return opts.GPUDevice
}

// deviceFree is free memory on the device a load with these options will
// land on, or 0 when the load is going to the CPU or the backend does not
// report it.
func deviceFree(opts *transcribe.LoadOptions) uint64 {
	if opts != nil && opts.Backend == transcribe.BackendCPU {
		return 0
	}
	devices, err := transcribe.Devices()
	if err != nil {
		return 0
	}
	i := deviceIndex(opts)
	if i < 0 || i >= len(devices) {
		return 0
	}
	return devices[i].MemoryFree
}

// CompiledKernels is how many compute kernels this model's device has built
// since the process started, or 0 on a backend that does not report it.
//
// The backend compiles a kernel the first time a graph needs a shape no
// earlier graph did, which is the cost a warmup exists to pay. So a rise
// across a transcription means that transcription was still compiling, and
// the warmup did not cover what the user just said. It is the difference
// between knowing and guessing about warmth, and it costs a device query.
func (m *Model) CompiledKernels() uint64 {
	return transcribe.CompiledKernels(m.device)
}

// CompiledKernelNames are those kernels, in the order they were built, or nil
// on a backend that does not report them.
//
// The count says a shape was new; these say what was new about it. A backend
// picks among variants of one operation by the dimensions it is given, so the
// names distinguish which matmul tile size a length selected and whether its
// dimensions took the aligned path. That is the band structure the warmup
// buckets have to cover, read off the backend instead of inferred from a sweep.
func (m *Model) CompiledKernelNames() []string {
	return transcribe.CompiledKernelNames(m.device)
}

// placement decides where compute runs, and names the device it chose.
//
// The library takes the first GPU it finds, integrated or not, so on a hybrid
// laptop it can land on the Intel chip rather than the discrete card. An iGPU
// shares memory bandwidth with the CPU it would be replacing and is no clear
// win, so pick the discrete one explicitly and stay on the CPU when there is
// none. DIKTAT_GPU overrides: 0 forces CPU, 1 takes whatever the library
// would have picked unaided, including an integrated GPU.
func placement() (*transcribe.LoadOptions, string, error) {
	if v, err := strconv.ParseBool(os.Getenv("DIKTAT_GPU")); err == nil {
		if !v {
			return &transcribe.LoadOptions{Backend: transcribe.BackendCPU}, "", nil
		}
		return nil, "auto", nil
	}

	devices, err := transcribe.Devices()
	if err != nil {
		return nil, "", err
	}
	i := discrete(devices)
	if i < 0 {
		return &transcribe.LoadOptions{Backend: transcribe.BackendCPU}, "", nil
	}
	// Index 0 is not selectable explicitly, since zero means auto, but a
	// device that is first in probe order is what auto picks anyway.
	if i == 0 {
		return nil, devices[0].Description, nil
	}
	return &transcribe.LoadOptions{GPUDevice: i}, devices[i].Description, nil
}

// discrete is the index of the first discrete GPU, or -1 when the machine has
// none. Integrated devices are skipped rather than ranked, for the reason
// placement gives.
func discrete(devices []transcribe.Device) int {
	for i, d := range devices {
		if d.Type == transcribe.DeviceGPU {
			return i
		}
	}
	return -1
}

// Device is one compute device as the library reports it.
//
// Kind and Type are both here and are not the same question. Kind is the
// backend that owns it, so two devices on one machine both read "vulkan";
// Type is gpu, igpu, cpu or accel, which is what placement branches on. A
// report that carried only Kind could not show a hybrid laptop's integrated
// chip beside its discrete one, which is the case the policy exists for.
type Device struct {
	Index                   int
	Name, Description, Kind string
	Type                    string
	MemoryTotal, MemoryFree uint64
}

// Devices lists what the backends registered, for a report on a machine
// nobody here can run. The policy that picks among them is Chosen; this is
// the evidence behind it, including devices the policy skipped.
func Devices() ([]Device, error) {
	quiet.Do(keepComplaints)
	devices, err := transcribe.Devices()
	if err != nil {
		return nil, err
	}
	out := make([]Device, len(devices))
	for i, d := range devices {
		out[i] = Device{
			Index: i, Name: d.Name, Description: d.Description, Kind: d.Kind,
			Type:        d.Type.String(),
			MemoryTotal: d.MemoryTotal, MemoryFree: d.MemoryFree,
		}
	}
	return out, nil
}

// Chosen names the device a load would land on now, or "" for the CPU. It
// answers through placement rather than beside it, so a report cannot
// disagree with what a load actually does.
//
// Both this and Devices take the library's narration off stderr first, the
// same as Load. They are the only other callers that reach a backend, and
// without it the first device query of the process prints ggml's device
// summary into the middle of whatever was being written.
func Chosen() (string, error) {
	quiet.Do(keepComplaints)
	_, name, err := placement()
	return name, err
}

// Name is the model, as the file it was loaded from calls it.
func (m *Model) Name() string { return m.name }

// OnGPU reports whether compute landed on a GPU. Worth asking because the
// costs that make a freshly loaded model slow, compiling shaders and
// allocating device buffers, exist only there: on the CPU a first
// transcription is measurably the same as the tenth.
func (m *Model) OnGPU() bool { return m.gpu != "" }

// Arch names the model and the device it runs on, because the difference
// between GPU and CPU here is two orders of magnitude on the encoder and
// falling back is silent. The device is named as the driver describes it, so
// landing on the wrong chip of a hybrid laptop shows up as that chip rather
// than hiding behind a plain "gpu".
func (m *Model) Arch() string {
	where := "cpu"
	if m.gpu != "" {
		where = m.gpu
	}
	return m.name + " on " + where
}

func (m *Model) Close() {
	if m.s != nil {
		m.s.Close()
		m.s = nil
	}
}

// Transcribe runs the model over 16 kHz mono samples. Whisper pads every
// utterance to a 30 second window internally, so its cost is roughly flat
// however long the utterance was; moonshine encodes only what it was given.
//
// Cancelling ctx gives up between decode steps and returns ErrAborted, which
// is what lets a background rehearsal get out of the way of something the
// user is waiting for. It is not immediate: the encoder cannot be interrupted,
// and on everything but an audio-LLM that is where the time goes.
func (m *Model) Transcribe(ctx context.Context, audio []float32) (string, error) {
	if len(audio) == 0 {
		return "", nil
	}
	res, err := m.Run(ctx, audio, nil)
	if err != nil {
		return "", err
	}
	return dropAnnotations(res.Text), nil
}

// Run is the whole of what the library reported: segments, words, speaker
// rows, and the timings. Dictation wants one string and gets it from
// Transcribe; a diarized transcript needs who spoke when, which is only here.
//
// opts nil is the library's defaults, which is what dictation runs.
func (m *Model) Run(ctx context.Context, audio []float32, opts *transcribe.RunOptions) (transcribe.Result, error) {
	// Only a clip longer than any before it allocates, because the compute
	// buffers are kept at the high-water mark and reused for anything shorter.
	// So that is the only run worth reading the device across, and what it
	// drops by is this model's graph, attributable to it rather than to
	// everything loaded since.
	clip := time.Duration(len(audio)) * time.Second / sampleRate
	m.mu.Lock()
	grow := clip > m.longest
	m.mu.Unlock()
	var before uint64
	if grow {
		before = m.free()
	}
	t0 := time.Now()
	res, err := m.s.Run(ctx, audio, opts)
	if grow {
		// Counted even when the run was aborted, because ggml allocates the
		// graph before it runs it, so the memory went whether or not a
		// transcript came back. The length it bought is not counted, though:
		// an abort may have stopped short of the shape this clip would have
		// reached, and charging its full cost to a shorter clip errs towards
		// a lower limit, which is the safe way to be wrong.
		after := m.free()
		m.mu.Lock()
		if before > after {
			m.graph += before - after
		}
		if err == nil || errors.Is(err, ErrTruncated) {
			m.longest = clip
		}
		m.mu.Unlock()
	}
	if err != nil {
		return transcribe.Result{}, err
	}
	t := Timings{Mel: res.Timings.Mel, Encode: res.Timings.Encode, Decode: res.Timings.Decode}
	t.Other = time.Since(t0) - t.Mel - t.Encode - t.Decode
	m.mu.Lock()
	m.timings = t
	m.mu.Unlock()
	return res, nil
}

// dropAnnotations removes non-speech markers. Whisper emits things like
// [BLANK_AUDIO], (wind blowing) or a bare musical note for audio it hears no
// words in. Moonshine returns an empty string for the same input, and the
// daemon only skips typing on empty, so without this a silent capture types
// "[BLANK_AUDIO]" into whatever has focus.
func dropAnnotations(text string) string {
	out := strings.Join(strings.Fields(text), " ")
	for {
		trimmed := annotation.ReplaceAllString(out, "")
		trimmed = strings.TrimSpace(strings.Join(strings.Fields(trimmed), " "))
		if trimmed == out {
			break
		}
		out = trimmed
	}
	return out
}

// Bracketed or parenthesised asides, and the note glyphs whisper uses for
// music. Deliberately not anchored: an annotation can sit beside real speech.
var annotation = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|[♪♫]`)
