// Package silence shortens the long gaps in a recording before a model is
// asked to transcribe it, and puts the time back afterwards.
//
// A model is paid by the second of audio, and dead air costs the same as
// speech. Measured on one machine, per second of audio: the voice activity
// detector costs 2.0 ms, parakeet 9.2, and the multitalker 43.9. So detecting
// where the speech is and dropping what is between costs about a fifth of what
// transcribing it does at the cheap end and a twentieth at the dear end, and
// the recording only has to be a fifth silence for that to pay -- less against
// a slower model, and nothing at all where the detector is already running to
// keep a chair from being counted as a speaker.
//
// Nothing is removed outright. A gap is shortened to a pause, because a hard
// splice puts two unrelated utterances against each other and models hear
// that: what a person says after a long think is not a continuation of what
// they said before it.
package silence

import "time"

// Span is a stretch of the recording, in recording time.
type Span struct {
	Start, End time.Duration
}

// Timeline maps the shortened recording's time back to the original. Every
// stamp a model returns is in the time it was given, and everything downstream
// of here -- the paragraph rule, the speaker join, the timestamps in the
// document -- is about the recording somebody made.
type Timeline struct {
	// cuts, in shortened time, each with what was taken out at that point.
	// Sorted, and the removals accumulate.
	at      []time.Duration
	removed []time.Duration
}

// Original is what a stamp in the shortened recording was in the original.
func (t Timeline) Original(d time.Duration) time.Duration {
	total := time.Duration(0)
	for i, at := range t.at {
		if d < at {
			break
		}
		total = t.removed[i]
	}
	return d + total
}

// Removed is how much audio was taken out altogether, which is what the saving
// is measured in.
func (t Timeline) Removed() time.Duration {
	if len(t.removed) == 0 {
		return 0
	}
	return t.removed[len(t.removed)-1]
}

// Compress shortens every gap between speech regions to at most `longest` and
// returns the audio and the map back.
//
// The cut is taken from the middle of a gap, leaving half the pause on each
// side of it: the detector's edges are tight to the speech, so cutting next to
// one would clip a breath or an onset, and cutting in the middle of a silence
// cannot.
//
// Regions are expected in order and non-overlapping, which is what a detector
// produces. A `longest` of zero returns the audio untouched, since a pause of
// nothing is a splice.
func Compress(samples []float32, rate int, speech []Span, longest time.Duration) ([]float32, Timeline) {
	if longest <= 0 || len(speech) == 0 || rate <= 0 {
		return samples, Timeline{}
	}
	at := func(d time.Duration) int {
		n := int(d * time.Duration(rate) / time.Second)
		if n < 0 {
			return 0
		}
		if n > len(samples) {
			return len(samples)
		}
		return n
	}

	out := make([]float32, 0, len(samples))
	var line Timeline
	removed := time.Duration(0)
	keep := 0 // where the kept audio resumes, in original samples

	// The gaps are before the first region, between regions, and after the
	// last. All three are somebody not talking.
	edges := make([]Span, 0, len(speech)+1)
	edges = append(edges, Span{Start: 0, End: speech[0].Start})
	for i := 1; i < len(speech); i++ {
		edges = append(edges, Span{Start: speech[i-1].End, End: speech[i].Start})
	}
	edges = append(edges, Span{Start: speech[len(speech)-1].End,
		End: time.Duration(len(samples)) * time.Second / time.Duration(rate)})

	for _, gap := range edges {
		if gap.End-gap.Start <= longest {
			continue
		}
		// Keep half the pause on each side and drop what is between.
		cutFrom := at(gap.Start + longest/2)
		cutTo := at(gap.End - (longest - longest/2))
		if cutTo <= cutFrom {
			continue
		}
		out = append(out, samples[keep:cutFrom]...)
		removed += time.Duration(cutTo-cutFrom) * time.Second / time.Duration(rate)
		line.at = append(line.at, time.Duration(len(out))*time.Second/time.Duration(rate))
		line.removed = append(line.removed, removed)
		keep = cutTo
	}
	out = append(out, samples[keep:]...)
	return out, line
}
