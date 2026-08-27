// Package audio captures 16kHz mono float32 audio via miniaudio (malgo).
package audio

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/gen2brain/malgo"

	"github.com/christian-oudard/diktat/internal/wav"
)

// SampleRate is 16 kHz mono, what moonshine and whisper both expect.
const SampleRate = wav.SampleRate

// RunawayGuard bounds a single capture. It is not a policy about how long
// anyone may speak: how long an utterance may usefully be is the model's
// answer, and the daemon asks it. This is the backstop for a device that
// reports one frame rate and delivers another, where the buffer would
// otherwise grow until the machine noticed. An hour of 16 kHz mono float32 is
// 230 MB, which is survivable and far past any dictation. Samples past it are
// dropped.
const RunawayGuard = time.Hour

// maxSamples is that guard in samples. A var rather than a const so a test
// can shrink it: filling an hour-sized buffer for real would allocate 230 MB
// to prove a bounds check.
var maxSamples = SampleRate * int(RunawayGuard/time.Second)

// teardownWait is how long the audio stack needs to let go of a device after
// the last stream on it closes. Measured on PipeWire with a bluetooth headset:
// the card's profile drops between one and two seconds after diktat's stream
// goes away. Reopening inside that window would be handed the same dead
// transport back, which is the whole thing Rebuild exists to get rid of.
const teardownWait = 2 * time.Second

// Recorder owns a miniaudio context and capture device. Start begins
// accumulating samples; Stop returns and clears them.
type Recorder struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device

	mu     sync.Mutex
	active bool
	buf    []int16
	level  float64
}

// NewRecorder initializes the miniaudio context and a 16kHz mono 16-bit
// capture device. Recording is gated by Start, which only arms the callback:
// the device runs and delivers audio for the whole session, and what Start
// changes is whether any of it is kept.
//
// So the audio stack sees a capture stream held open from daemon start to
// daemon exit, which pins a bluetooth headset into its headset profile the
// entire time. That is the right trade here, since renegotiating the profile
// costs a second or two and a dictation tool is always about to want the
// microphone, but see Rebuild for what it costs.
func NewRecorder() (*Recorder, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("init malgo context: %w", err)
	}

	r := &Recorder{ctx: ctx}
	if err := r.open(); err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return nil, err
	}
	return r, nil
}

// open initializes and starts a capture device on the current default input.
// Separate from NewRecorder because Rebuild does the same thing again, on the
// same context: the context is the audio backend, which is not what breaks.
func (r *Recorder) open() error {
	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.SampleRate = SampleRate
	cfg.Alsa.NoMMap = 1

	dev, err := malgo.InitDevice(r.ctx.Context, cfg, malgo.DeviceCallbacks{Data: r.capture})
	if err != nil {
		return fmt.Errorf("init capture device: %w", err)
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		return fmt.Errorf("start capture device: %w", err)
	}
	r.mu.Lock()
	r.device = dev
	r.mu.Unlock()
	return nil
}

// capture is the device callback, accumulating into buf while armed.
func (r *Recorder) capture(_, in []byte, frameCount uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return
	}
	// Trust the frame count only as far as the buffer goes. This is a
	// callback from C during a dictation, so reading past the end is not an
	// error value, it is the daemon disappearing mid-sentence.
	if n := len(in) / 2; n < int(frameCount) {
		frameCount = uint32(n)
	}
	samples := make([]int16, frameCount)
	var peak int32
	for i := range samples {
		s := int16(binary.LittleEndian.Uint16(in[2*i:]))
		samples[i] = s
		// In int32, because negating the most negative int16 gives itself
		// back: a full-scale negative sample would read as quieter than
		// silence and the meter would ignore the loudest thing in the frame.
		a := int32(s)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	r.appendSamples(samples)
	r.level = float64(peak) / full
}

// Rebuild closes the capture device and opens a new one, which is how an
// input that has gone bit-exact silent comes back.
//
// What goes silent is a bluetooth headset's SCO link, and holding the device
// open is what exposes it: WirePlumber re-evaluates the card's profile
// whenever playback starts or stops, diktat's stream pins that profile, and
// the link can end up down on the host while the headset still holds it. The
// kernel says "SCO packet for unknown connection handle" once and nothing
// mentions it again. The source stays RUNNING and unmuted and delivers zeros
// for the rest of the session. Do not look for a cleaner signal in the
// stack's own state: bluez5.profile on the source node reads "off" while the
// link is dead, and reads "off" on a healthy one too.
//
// Closing the last stream on a device is what makes the audio stack tear the
// device down, and that teardown is the repair: on a bluetooth headset it
// drops the card's profile, so opening again negotiates a fresh HFP link in
// place of the dead one. Measured to be the same fix as bouncing the profile
// by hand with pactl, without diktat having to know that bluetooth, profiles
// or pactl exist.
//
// It cannot report whether the rebuild worked. Confirming that would mean
// listening to the new device and expecting signal, and a headset that gates
// its own silence to bit-exact zero gives none until someone speaks; see
// IsDead. What can be checked is the link itself, which internal/sco reads
// directly, and which the daemon looks at on a ticker: a rebuild that did not
// restore it leaves the link still missing.
//
// The caller must not be recording: the device the capture is arriving on is
// the one being closed.
func (r *Recorder) Rebuild() error {
	r.device.Uninit()
	time.Sleep(teardownWait)
	return r.open()
}

// appendSamples accumulates up to maxSamples, dropping the rest. Callers hold
// r.mu.
func (r *Recorder) appendSamples(samples []int16) {
	room := maxSamples - len(r.buf)
	if room <= 0 {
		return
	}
	if room < len(samples) {
		samples = samples[:room]
	}
	r.buf = append(r.buf, samples...)
}

// Start arms the recorder. Subsequent device callbacks accumulate into buf.
func (r *Recorder) Start() {
	r.mu.Lock()
	r.buf = r.buf[:0]
	r.active = true
	r.mu.Unlock()
}

// Stop disarms the recorder and returns the accumulated samples.
func (r *Recorder) Stop() []int16 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = false
	out := r.buf
	r.buf = nil
	return out
}

// Level returns the peak amplitude of the most recent capture callback, for a
// live meter while recording.
func (r *Recorder) Level() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.level
}

// Close stops the device and releases the context.
func (r *Recorder) Close() {
	if r.device != nil {
		r.device.Uninit()
	}
	if r.ctx != nil {
		_ = r.ctx.Uninit()
		r.ctx.Free()
	}
}
