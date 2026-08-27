package transcript

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Turn is one stretch of one speaker talking. Times are from the start of the
// whole recording, not of the piece it was transcribed in.
type Turn struct {
	Speaker    int
	Start, End time.Duration
	Text       string
}

// Row is one "who spoke when" row from a diarizer, in recording time. Rows may
// overlap, since two people can talk at once.
type Row struct {
	Speaker    int
	Start, End time.Duration
}

// Span is a piece of transcript with the audio it came from: one word from a
// model that stamps words, or a whole segment from one that stops there.
type Span struct {
	Start, End time.Duration
	Text       string
	// Stream is which of a model's per-speaker decodes this came from, and 0
	// for a model that decodes the audio once. It is not the speaker -- who
	// spoke is the rows' answer, below -- and exists only for Dedupe, which
	// has to tell one stream hearing a word twice from two streams hearing it
	// once each.
	Stream int
}

// Attribute says who spoke each span, by asking the speaker rows rather than
// the model that produced the text.
//
// This is the join, and it is what makes a recording longer than a model can
// hold transcribable at all. Cutting the audio renumbers the speakers in every
// piece, because a model labels them in order of first appearance and has no
// memory of the piece before; matching those numberings across a cut needs the
// same person heard on both sides of it, which for a workshop with a dozen
// voices fails often enough to be worthless. A diarizer that took the whole
// recording in one pass has one numbering for all of it, so the identity comes
// from there and the text comes from wherever the words are best.
//
// Attribution is per span rather than per segment because the two views cut in
// different places: a segment ends where the transcriber drew a sentence, and a
// row ends where somebody stopped talking, so a segment that spans an
// interruption belongs to two people. Given word spans this puts the change
// where it happened.
func Attribute(spans []Span, rows []Row) []Turn {
	if len(spans) == 0 || len(rows) == 0 {
		return nil
	}
	// In recording order, because a turn is a run of neighbouring words by one
	// speaker and neighbouring is a fact about time. A model that decodes one
	// stream per speaker hands its words back grouped that way instead, and
	// two people taking turns then come out as one turn each: everything the
	// first said, then everything the second did.
	spans = inOrder(spans)
	who := make([]int, len(spans))
	held := 0
	for i, s := range spans {
		who[i] = cover(s, rows)
		if who[i] == 0 {
			// No row covers this. A gap in the rows is the diarizer missing
			// speech rather than a new person, so whoever was talking still is.
			who[i] = held
		}
		held = who[i]
	}
	// Anything before the first row anyone was heard in, which the pass above
	// could not fill because nothing had been heard yet.
	first := 0
	for i := range who {
		if who[i] != 0 {
			first = who[i]
			for j := range who[:i] {
				who[j] = first
			}
			break
		}
	}
	if first == 0 {
		// No row covered any word at all: rows and words that do not overlap,
		// which happens when a diarizer heard speech somewhere the transcriber
		// found none, or the two were run on different audio. There is one
		// unattributed voice rather than a speaker numbered zero, which is what
		// the rest of this counts from one to avoid.
		for i := range who {
			who[i] = 1
		}
	}
	smooth(spans, who)
	return merge(spans, who)
}

// cover is the speaker whose rows overlap this span most, or 0 when none does.
// Ties go to the lower number, so the same input always attributes the same way.
func cover(s Span, rows []Row) int {
	best, at := time.Duration(0), 0
	seen := map[int]time.Duration{}
	for _, r := range rows {
		start, end := max(s.Start, r.Start), min(s.End, r.End)
		if end <= start {
			continue
		}
		seen[r.Speaker] += end - start
		if v := seen[r.Speaker]; v > best || (v == best && r.Speaker < at) {
			best, at = v, r.Speaker
		}
	}
	return at
}

// minTurn is the shortest stretch worth calling a change of speaker. Below it,
// a run bracketed by one person on both sides is taken to be that person.
//
// It exists because the two views disagree at their edges by a word or so: a
// row starts a moment late and the word that opened the sentence lands on
// whoever spoke before, which reads as an interjection nobody made. A second is
// under the shortest real turn here -- "yes", "mm-hm", a name -- and over the
// error, which is a word.
const minTurn = time.Second

// smooth removes changes of speaker too short to believe, in place.
func smooth(spans []Span, who []int) {
	for _, r := range runs(who) {
		if r.from == 0 || r.to == len(who) {
			continue // Nothing on one side to be bracketed by.
		}
		before, after := who[r.from-1], who[r.to]
		if before != after || before == who[r.from] {
			continue
		}
		if spans[r.to-1].End-spans[r.from].Start >= minTurn {
			continue
		}
		for i := r.from; i < r.to; i++ {
			who[i] = before
		}
	}
}

// runs are the stretches of consecutive spans by one speaker, as half-open
// index ranges.
func runs(who []int) []struct{ from, to int } {
	var out []struct{ from, to int }
	from := 0
	for i := 1; i <= len(who); i++ {
		if i == len(who) || who[i] != who[from] {
			out = append(out, struct{ from, to int }{from, i})
			from = i
		}
	}
	return out
}

// Pause is the silence that reads as a paragraph. Someone holding the floor for
// twenty minutes is one turn, and one turn is one paragraph, which is a wall of
// four hundred words nobody can find their place in. Where to break it is not
// ours to invent, and does not have to be: the speaker already did it, and the
// word stamps say where. Two seconds is longer than the gap between sentences
// and shorter than the pause before answering a question.
const Pause = 2 * time.Second

// Break reports whether a paragraph starts here: a pause, and a finished
// sentence before it. The pause alone is not enough. People stop mid-sentence
// to think, and the first draft of this broke a paragraph after "the movement
// of my being", which reads as a mistake in a way the wall of text does not.
func Break(before string, gap time.Duration) bool {
	if gap < Pause || before == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(before)
	return strings.ContainsRune(".?!…", last)
}

// merge glues consecutive spans by one speaker into a turn.
func merge(spans []Span, who []int) []Turn {
	var turns []Turn
	for _, r := range runs(who) {
		t := Turn{Speaker: who[r.from], Start: spans[r.from].Start, End: spans[r.to-1].End}
		for i := r.from; i < r.to; i++ {
			switch {
			case t.Text == "":
			case Break(t.Text, spans[i].Start-spans[i-1].End):
				t.Text += "\n\n"
			default:
				t.Text += " "
			}
			t.Text += spans[i].Text
		}
		turns = append(turns, t)
	}
	return turns
}

// echo is how far apart two decodes of the same audio stamp the same word. The
// family this is for separates speakers by masking the audio, and what leaks
// through arrives a little late: measured at 80 to 240 ms on a recording where
// one voice was split across two streams.
const echo = 300 * time.Millisecond

// Dedupe drops the copy a second stream made of a word another already has.
//
// A model that decodes one stream per speaker is separating them imperfectly,
// so a voice split across two streams is transcribed twice, and time-ordering
// those words interleaves the copies: "my fellow my fellow Americans
// Americans". Two streams are two hearings of the same audio, so the same word
// at the same moment from a different stream is one utterance, not two.
//
// Different stream is what makes this safe. Somebody saying "very, very" says
// it twice in one stream a quarter of a second apart, which is exactly what
// the timing rule alone would throw away.
func Dedupe(spans []Span) []Span {
	spans = inOrder(spans)
	kept := make([]Span, 0, len(spans))
	for _, s := range spans {
		dup := false
		for i := len(kept) - 1; i >= 0; i-- {
			if s.Start-kept[i].Start > echo {
				break
			}
			if kept[i].Stream != s.Stream && bare(kept[i].Text) == bare(s.Text) {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, s)
		}
	}
	return kept
}

// bare is a word with the things that do not make it a different word removed:
// case, and the punctuation a transcriber hung off it.
func bare(text string) string {
	return strings.ToLower(strings.Trim(text, " .,!?;:\"'"))
}

// inOrder is the spans by time. Stable, so words stamped at the same moment
// keep the order the model gave them, which is the only thing that says which
// came first.
func inOrder(spans []Span) []Span {
	out := slices.Clone(spans)
	slices.SortStableFunc(out, func(a, b Span) int { return cmp.Compare(a.Start, b.Start) })
	return out
}
