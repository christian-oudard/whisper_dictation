package audio

import (
	"encoding/binary"
	"testing"
)

// bytes packs samples the way the capture device delivers them.
func bytes16(samples ...int16) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(s))
	}
	return out
}

// A full-scale negative sample is the loudest a frame can be. Negating it in
// int16 gives it back unchanged and negative, so a meter that stays in int16
// reads the loudest possible frame as silence.
func TestLevelSeesFullScaleNegative(t *testing.T) {
	r := &Recorder{active: true}
	r.capture(nil, bytes16(-32768, 0, 0, 0), 4)
	if got := r.Level(); got < 0.99 {
		t.Errorf("Level = %v for a full-scale negative sample, want about 1", got)
	}
}

// The frame count comes from the audio stack and the buffer comes with it.
// Reading past the end of the buffer is a panic inside a callback from C,
// which is the daemon disappearing rather than an error anyone can handle.
func TestCaptureTrustsTheBufferNotTheCount(t *testing.T) {
	r := &Recorder{active: true}
	r.capture(nil, bytes16(1, 2), 1000) // says a thousand frames, brings two
	if got := len(r.Stop()); got != 2 {
		t.Errorf("kept %d samples, want the 2 that arrived", got)
	}
}

// Nothing is kept while the recorder is not armed: the device is held open for
// the whole session so a bluetooth headset stays in its headset profile, and
// everything said between dictations has to fall on the floor.
func TestCaptureIgnoresWhatIsNotRecorded(t *testing.T) {
	r := &Recorder{}
	r.capture(nil, bytes16(1000, 2000), 2)
	if got := len(r.Stop()); got != 0 {
		t.Errorf("kept %d samples while disarmed, want none", got)
	}
}
