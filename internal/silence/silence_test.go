package silence

import (
	"testing"
	"time"
)

const rate = 16000

// tone is audio a test can tell from silence, so a cut can be checked by what
// is left rather than only by how long it is.
func tone(d time.Duration, v float32) []float32 {
	out := make([]float32, int(d*rate/time.Second))
	for i := range out {
		out[i] = v
	}
	return out
}

func build(parts ...[]float32) []float32 {
	var out []float32
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func seconds(d time.Duration) float64 { return d.Seconds() }

// Speech, a thirty second think, speech. The think becomes a pause and the
// speech either side of it is untouched.
func TestCompressShortensTheGap(t *testing.T) {
	audio := build(tone(5*time.Second, 0.5), tone(30*time.Second, 0), tone(5*time.Second, 0.5))
	speech := []Span{{0, 5 * time.Second}, {35 * time.Second, 40 * time.Second}}

	out, moved, line := Compress(audio, rate, speech, time.Second)

	got := time.Duration(len(out)) * time.Second / rate
	if want := 11 * time.Second; got != want {
		t.Errorf("kept %v of audio, want %v", got, want)
	}
	if line.Removed() != 29*time.Second {
		t.Errorf("removed %v, want 29s", line.Removed())
	}
	// Both stretches of speech survive whole.
	var quiet, loud int
	for _, v := range out {
		if v == 0 {
			quiet++
		} else {
			loud++
		}
	}
	if want := 10 * rate; loud != want {
		t.Errorf("%d samples of speech survived, want %d", loud, want)
	}
	if want := 1 * rate; quiet != want {
		t.Errorf("%d samples of pause, want %d", quiet, want)
	}
	// And the second stretch is where the model will find it.
	if moved[1].Start != 6*time.Second {
		t.Errorf("the second stretch starts at %v in the shortened audio, want 6s", moved[1].Start)
	}
}

// The map back is what makes the rest of the program work on the recording
// somebody made rather than the one the model was handed.
func TestTimelinePutsTheTimeBack(t *testing.T) {
	audio := build(tone(5*time.Second, 0.5), tone(30*time.Second, 0), tone(5*time.Second, 0.5))
	speech := []Span{{0, 5 * time.Second}, {35 * time.Second, 40 * time.Second}}
	_, _, line := Compress(audio, rate, speech, time.Second)

	for _, c := range []struct{ shortened, original time.Duration }{
		{0, 0},
		{4 * time.Second, 4 * time.Second},   // before the cut, unchanged
		{6 * time.Second, 35 * time.Second},  // the far side of it
		{11 * time.Second, 40 * time.Second}, // the end
		{6500 * time.Millisecond, 35500 * time.Millisecond},
	} {
		if got := line.Original(c.shortened); got != c.original {
			t.Errorf("Original(%v) = %v, want %v", c.shortened, got, c.original)
		}
	}

	// And round trips for anything that was not inside a cut.
	for _, d := range []time.Duration{0, 2 * time.Second, 35 * time.Second, 39 * time.Second} {
		if got := line.Original(line.Shortened(d)); got != d {
			t.Errorf("%v became %v", d, got)
		}
	}
}

// A gap no longer than the pause is left alone, and a recording that is all
// speech comes back untouched.
func TestCompressLeavesShortGaps(t *testing.T) {
	audio := build(tone(5*time.Second, 0.5), tone(800*time.Millisecond, 0), tone(5*time.Second, 0.5))
	speech := []Span{{0, 5 * time.Second}, {5800 * time.Millisecond, 10800 * time.Millisecond}}

	out, _, line := Compress(audio, rate, speech, time.Second)
	if len(out) != len(audio) {
		t.Errorf("kept %.1fs of %.1fs", seconds(time.Duration(len(out))*time.Second/rate), seconds(time.Duration(len(audio))*time.Second/rate))
	}
	if line.Removed() != 0 {
		t.Errorf("removed %v from a recording with no long gap", line.Removed())
	}
}

// Silence before the first word and after the last is a gap like any other:
// a recorder left running is mostly that.
func TestCompressTrimsTheEnds(t *testing.T) {
	audio := build(tone(20*time.Second, 0), tone(5*time.Second, 0.5), tone(20*time.Second, 0))
	speech := []Span{{20 * time.Second, 25 * time.Second}}

	out, moved, line := Compress(audio, rate, speech, 2*time.Second)
	if got, want := time.Duration(len(out))*time.Second/rate, 9*time.Second; got != want {
		t.Errorf("kept %v, want %v", got, want)
	}
	if moved[0].Start != 2*time.Second {
		t.Errorf("the speech starts at %v, want 2s", moved[0].Start)
	}
	if line.Original(moved[0].Start) != 20*time.Second {
		t.Errorf("it maps back to %v, want 20s", line.Original(moved[0].Start))
	}
}

// Nothing to go on is nothing done: no regions, or a pause of zero, which
// would be a splice rather than a pause.
func TestCompressDoesNothingWithoutRegions(t *testing.T) {
	audio := tone(10*time.Second, 0.5)
	for _, c := range []struct {
		what    string
		speech  []Span
		longest time.Duration
	}{
		{"no regions", nil, time.Second},
		{"no pause", []Span{{0, time.Second}}, 0},
	} {
		out, _, line := Compress(audio, rate, c.speech, c.longest)
		if len(out) != len(audio) || line.Removed() != 0 {
			t.Errorf("%s: cut %v", c.what, line.Removed())
		}
	}
}
