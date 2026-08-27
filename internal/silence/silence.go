// Package silence shortens the long gaps in a recording before a model is
// asked to transcribe it, and puts the time back afterwards.
//
// A model is paid by the second of audio and dead air costs the same as
// speech. Measured on one machine, per second of audio: the voice activity
// detector 2.0 ms, parakeet 9.2, the multitalker 43.9. So finding the speech
// and shortening what is between costs a fifth of what transcribing it does at
// the cheap end and a twentieth at the dear end, and the recording only has to
// be a fifth silence for that to pay. Where the detector is already running to
// keep a chair from being counted as a speaker, it is free.
//
// Nothing is removed outright. A gap becomes a pause, because a hard splice
// puts two unrelated utterances against each other and models hear that: what
// somebody says after a long think is not a continuation of what they said
// before it.
package silence

import (
	"math"
	"time"
)

// Span is a stretch of the recording.
type Span struct {
	Start, End time.Duration
}

// Timeline maps between the shortened recording and the original. Every stamp
// a model returns is in the time it was given, and everything downstream --
// the paragraph rule, the speaker join, the timestamps in the document -- is
// about the recording somebody made.
type Timeline struct {
	// at[i] is where a cut lands in shortened time, and removed[i] is how much
	// audio has been taken out by that point. Both ascending.
	at      []time.Duration
	removed []time.Duration
}

// Original is what a stamp in the shortened recording was in the original.
func (t Timeline) Original(d time.Duration) time.Duration {
	shift := time.Duration(0)
	for i, at := range t.at {
		if d < at {
			break
		}
		shift = t.removed[i]
	}
	return d + shift
}

// Shortened is where a moment in the original recording ended up, for the
// speech regions, which are found before the cutting and used after it.
//
// A moment inside a cut has no answer and gets the cut's own position, which
// is where the audio either side of it now meets.
func (t Timeline) Shortened(d time.Duration) time.Duration {
	shift := time.Duration(0)
	for i, at := range t.at {
		if d-t.removed[i] < at {
			break
		}
		shift = t.removed[i]
	}
	if out := d - shift; out > 0 {
		return out
	}
	return 0
}

// Removed is how much audio was taken out altogether.
func (t Timeline) Removed() time.Duration {
	if len(t.removed) == 0 {
		return 0
	}
	return t.removed[len(t.removed)-1]
}

// Compress shortens every gap between speech regions to at most longest, and
// returns the audio, where the speech ended up, and the map back.
//
// The cut comes out of the middle of a gap, leaving half the pause on each
// side: a detector's edges are tight to the speech, so cutting next to one
// clips a breath or an onset, and cutting in the middle of a silence cannot.
//
// Regions are expected in order and not overlapping, which is what a detector
// produces. A longest of zero returns the audio untouched, since a pause of
// nothing is a splice.
func Compress(samples []float32, rate int, speech []Span, longest time.Duration) ([]float32, []Span, Timeline) {
	if longest <= 0 || len(speech) == 0 || rate <= 0 {
		return samples, speech, Timeline{}
	}
	clip := time.Duration(len(samples)) * time.Second / time.Duration(rate)
	sampleAt := func(d time.Duration) int {
		n := int(d * time.Duration(rate) / time.Second)
		return min(max(n, 0), len(samples))
	}

	// The gaps are before the first region, between regions, and after the
	// last. All three are nobody talking.
	gaps := make([]Span, 0, len(speech)+1)
	gaps = append(gaps, Span{End: speech[0].Start})
	for i := 1; i < len(speech); i++ {
		gaps = append(gaps, Span{Start: speech[i-1].End, End: speech[i].Start})
	}
	gaps = append(gaps, Span{Start: speech[len(speech)-1].End, End: clip})

	floor := level(samples, rate, speech)
	out := make([]float32, 0, len(samples))
	var line Timeline
	removed := time.Duration(0)
	kept := 0 // where the audio to keep resumes, in original samples
	for _, gap := range gaps {
		if gap.End-gap.Start <= longest {
			continue
		}
		from := sampleAt(gap.Start + longest/2)
		to := sampleAt(gap.End - (longest - longest/2))
		// Only what is quiet as well as unmarked. A detector deciding where to
		// look is allowed to be strict, because a false yes there costs an
		// extra speaker; a rule deciding what to delete has to be lenient,
		// because a false yes here costs somebody's words. Measured: the
		// detector missed the opening of a meeting and the whole greeting went
		// with the silence before it.
		from, to = quiet(samples, rate, from, to, floor)
		if to <= from || from < kept || time.Duration(to-from)*time.Second/time.Duration(rate) < longest {
			continue
		}
		out = append(out, samples[kept:from]...)
		removed += time.Duration(to-from) * time.Second / time.Duration(rate)
		line.at = append(line.at, time.Duration(len(out))*time.Second/time.Duration(rate))
		line.removed = append(line.removed, removed)
		kept = to
	}
	out = append(out, samples[kept:]...)

	moved := make([]Span, len(speech))
	for i, s := range speech {
		moved[i] = Span{Start: line.Shortened(s.Start), End: line.Shortened(s.End)}
	}
	return out, moved, line
}

// frame is the granularity the loudness rule works at. Short enough that a
// word is several of them, long enough that one glottal stop is not a frame.
const frame = 50 * time.Millisecond

// level is the loudness below which audio is nothing worth keeping, taken
// from the recording rather than assumed: a hundredth of what this speech is,
// which is 40 dB down. A recording with no speech in it has no scale, and
// gets a floor of zero, so only true digital silence is cut.
func level(samples []float32, rate int, speech []Span) float64 {
	var sum float64
	var n int
	for _, s := range speech {
		from := int(s.Start * time.Duration(rate) / time.Second)
		to := min(int(s.End*time.Duration(rate)/time.Second), len(samples))
		for i := from; i < to; i++ {
			sum += float64(samples[i]) * float64(samples[i])
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return 0.01 * math.Sqrt(sum/float64(n))
}

// quiet narrows a range to the part of it that is actually quiet, dropping
// frames at either end that are not. What is left in the middle may still hold
// a loud frame, and that is the trade: cutting a thirty second gap around one
// cough is worth more than keeping the cough.
func quiet(samples []float32, rate, from, to int, floor float64) (int, int) {
	if floor <= 0 {
		return from, to
	}
	width := int(frame) * rate / int(time.Second)
	loud := func(at int) bool {
		end := min(at+width, to)
		var sum float64
		for i := at; i < end; i++ {
			sum += float64(samples[i]) * float64(samples[i])
		}
		if end <= at {
			return false
		}
		return math.Sqrt(sum/float64(end-at)) > floor
	}
	for from+width <= to && loud(from) {
		from += width
	}
	for to-width >= from && loud(to-width) {
		to -= width
	}
	return from, to
}
