package transcript

import (
	"math"
	"testing"
	"time"
)

// tone fills n samples with a loud waveform, so silence and speech can be told
// apart without a recording.
func tone(n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(math.Sin(float64(i) / 8))
	}
	return out
}

func quiet(n int) []float32 { return make([]float32, n) }

func join(parts ...[]float32) []float32 {
	var out []float32
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// The cut goes in the silence, not at the limit. This is the whole reason the
// package exists: a cut through a sentence splits it into two fragments that
// no later pass can put back together.
func TestCutsLandInSilence(t *testing.T) {
	// Speech, a second of silence, then more speech. With a limit of 12s and
	// a 6s search, the quiet second is the only sensible place to cut.
	audio := join(tone(9*SampleRate), quiet(SampleRate), tone(9*SampleRate))
	cuts := Cuts(audio, 12*time.Second, 6*time.Second)
	if len(cuts) != 2 {
		t.Fatalf("Cuts gave %d pieces, want 2", len(cuts))
	}
	at := time.Duration(cuts[1]) * time.Second / SampleRate
	if at < 9*time.Second || at > 10*time.Second {
		t.Errorf("cut at %v, want inside the silence at 9s-10s", at)
	}
}

// Audio shorter than the limit is not cut at all.
func TestShortAudioIsOnePiece(t *testing.T) {
	if got := Cuts(tone(5*SampleRate), time.Minute, 10*time.Second); len(got) != 1 || got[0] != 0 {
		t.Errorf("Cuts = %v, want [0]", got)
	}
}

// Speech that never lets up still has to be cut, since the limit is where the
// card runs out rather than a preference.
func TestUnbrokenSpeechIsStillCut(t *testing.T) {
	cuts := Cuts(tone(30*SampleRate), 10*time.Second, 3*time.Second)
	if len(cuts) < 3 {
		t.Errorf("Cuts gave %d pieces of 30s at a 10s limit, want at least 3", len(cuts))
	}
}

// The link carries a speaker's identity across a cut even though the model
// numbered them differently on each side.
// Two speakers in the new piece must never collapse onto one in the old,
// which would merge two people into one voice for the rest of the document.
// Someone silent through the shared stretch cannot be matched, and inventing a
// match for them would be worse than admitting it.
