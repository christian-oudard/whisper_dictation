// Package transcript turns a recording into a document that says who spoke.
//
// It exists because the model that can represent this kind of recording
// cannot hold one: MOSS is the only entry with no speaker cap, and it holds
// ~130 MiB per audio minute, so an hour of it does not fit on a laptop card.
// Cutting is therefore not an optimisation, it is the only way to run it at
// all, and everything here is about cutting without damaging the transcript.
package transcript

import "time"

// SampleRate is what every model here is fed at.
const SampleRate = 16000

// Cuts divides a recording into pieces no longer than limit, each cut placed
// where the recording is quietest rather than at a fixed offset.
//
// Where the cut falls is the whole game. Cutting a 60 second clip at 30 gave
// "was henceforth to be the victim." followed by "of a strange mystery.", and
// that is a cut through the middle of a sentence. A cut through a silence
// costs nothing, because no word crosses it, so this looks backwards from the
// limit for the quietest window and cuts there.
//
// It returns the sample index each piece starts at, beginning with 0.
func Cuts(samples []float32, limit, search time.Duration) []int {
	span := int(limit.Seconds() * SampleRate)
	back := int(search.Seconds() * SampleRate)
	if span <= 0 || len(samples) <= span {
		return []int{0}
	}
	if back > span {
		back = span
	}

	cuts := []int{0}
	for {
		at := cuts[len(cuts)-1]
		if len(samples)-at <= span {
			return cuts
		}
		next := quietest(samples, at+span-back, at+span)
		// A window with no quiet in it still has to be cut somewhere, and the
		// limit is where the memory runs out, so that is the fallback.
		if next <= at {
			next = at + span
		}
		cuts = append(cuts, next)
	}
}

// quietest is the middle of the least energetic window in samples[from:to],
// which is where a cut does the least damage. Windows are a fifth of a second,
// which is longer than the gap inside a word and shorter than the gap between
// two speakers.
func quietest(samples []float32, from, to int) int {
	const window = SampleRate / 5
	if from < 0 {
		from = 0
	}
	if to > len(samples) {
		to = len(samples)
	}
	if to-from < window {
		return 0
	}
	best, bestAt := -1.0, 0
	for i := from; i+window <= to; i += window / 2 {
		var sum float64
		for _, s := range samples[i : i+window] {
			sum += float64(s) * float64(s)
		}
		if best < 0 || sum < best {
			best, bestAt = sum, i+window/2
		}
	}
	return bestAt
}
