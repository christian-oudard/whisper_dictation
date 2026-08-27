package transcript

import (
	"testing"
	"time"
)

func sec(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// word is one span of the recording with a word on it.
func word(text string, from, to float64) Span {
	return Span{Start: sec(from), End: sec(to), Text: text}
}

// A word is attributed to whoever the rows say was talking through it, not to
// whoever the sentence around it belongs to. This is the join.
func TestAttributeFollowsTheRows(t *testing.T) {
	spans := []Span{
		word("what", 0, 1), word("about", 1, 2), word("this", 2, 3),
		word("I", 4, 5), word("agree", 5, 6), word("completely", 6, 7),
	}
	rows := []Row{
		{Speaker: 1, Start: sec(0), End: sec(3)},
		{Speaker: 2, Start: sec(3.5), End: sec(7)},
	}
	turns := Attribute(spans, rows)
	if len(turns) != 2 {
		t.Fatalf("Attribute gave %d turns, want 2: %v", len(turns), turns)
	}
	if turns[0].Speaker != 1 || turns[0].Text != "what about this" {
		t.Errorf("first turn = %+v, want S1 saying all three words", turns[0])
	}
	if turns[1].Speaker != 2 || turns[1].Text != "I agree completely" {
		t.Errorf("second turn = %+v, want S2 saying all three words", turns[1])
	}
}

// A sentence that runs across a change of speaker is split where the change
// happened. A model's own segments cut where it drew a sentence, so nothing but
// the rows can put the break here.
func TestAttributeSplitsASentenceAcrossSpeakers(t *testing.T) {
	spans := []Span{
		word("so", 0, 1), word("what", 1, 2),
		word("no", 3, 4.5), word("thank", 4.5, 5.5), word("you", 5.5, 6),
	}
	rows := []Row{
		{Speaker: 1, Start: sec(0), End: sec(2)},
		{Speaker: 2, Start: sec(3), End: sec(6)},
	}
	turns := Attribute(spans, rows)
	if len(turns) != 2 || turns[1].Text != "no thank you" {
		t.Fatalf("Attribute = %v, want the second speaker's three words together", turns)
	}
}

// A word the diarizer heard nobody in belongs to whoever was already talking.
// A gap in the rows is the diarizer missing speech, not a new person.
func TestAttributeCarriesThroughGaps(t *testing.T) {
	spans := []Span{word("one", 0, 1), word("two", 1, 2), word("three", 2, 3)}
	rows := []Row{{Speaker: 3, Start: sec(0), End: sec(1)}}
	turns := Attribute(spans, rows)
	if len(turns) != 1 || turns[0].Speaker != 3 {
		t.Fatalf("Attribute = %v, want one turn by S3", turns)
	}
}

// A speaker change shorter than a word or two, bracketed by one person on both
// sides, is the two views disagreeing at an edge rather than an interjection.
func TestAttributeIgnoresAFlicker(t *testing.T) {
	spans := []Span{
		word("I", 0, 1), word("think", 1, 2), word("that", 2, 2.4),
		word("we", 2.4, 3), word("should", 3, 4),
	}
	// A row for someone else over the middle word, and the presenter's own rows
	// stopping either side of it, so that word really is attributed away.
	rows := []Row{
		{Speaker: 1, Start: sec(0), End: sec(2.05)},
		{Speaker: 2, Start: sec(2.1), End: sec(2.35)},
		{Speaker: 1, Start: sec(2.5), End: sec(4)},
	}
	turns := Attribute(spans, rows)
	if len(turns) != 1 {
		t.Fatalf("Attribute gave %d turns, want 1: %v", len(turns), turns)
	}
}

// A real short answer is kept. "Yes" between two of the presenter's sentences
// is the whole content of some turns in a workshop.
func TestAttributeKeepsAShortAnswer(t *testing.T) {
	spans := []Span{
		word("do", 0, 1), word("you", 1, 2),
		word("yes", 2.2, 3.6),
		word("good", 4, 5),
	}
	rows := []Row{
		{Speaker: 1, Start: sec(0), End: sec(2)},
		{Speaker: 2, Start: sec(2.1), End: sec(3.8)},
		{Speaker: 1, Start: sec(3.9), End: sec(5)},
	}
	turns := Attribute(spans, rows)
	if len(turns) != 3 {
		t.Fatalf("Attribute gave %d turns, want 3: %v", len(turns), turns)
	}
	if turns[1].Speaker != 2 || turns[1].Text != "yes" {
		t.Errorf("middle turn = %+v, want S2 saying yes", turns[1])
	}
}

// Speech before the first row belongs to whoever is heard first, rather than to
// a speaker 0 that no renderer knows what to do with.
func TestAttributeFillsTheOpening(t *testing.T) {
	spans := []Span{word("hello", 0, 1), word("everyone", 1, 2)}
	rows := []Row{{Speaker: 2, Start: sec(1.5), End: sec(2)}}
	turns := Attribute(spans, rows)
	if len(turns) != 1 || turns[0].Speaker != 2 {
		t.Fatalf("Attribute = %v, want one turn by S2", turns)
	}
}

// Nothing to join to is not an empty transcript with confident labels on it.
func TestAttributeWithoutRows(t *testing.T) {
	if got := Attribute([]Span{word("hello", 0, 1)}, nil); got != nil {
		t.Errorf("Attribute without rows = %v, want nil", got)
	}
}

// A pause inside a turn is a paragraph. Twenty minutes of one person is one
// turn, and one turn as one paragraph is a wall nobody can read.
func TestAttributeBreaksOnAPause(t *testing.T) {
	spans := []Span{
		word("first", 0, 1), word("thought.", 1, 2),
		word("second", 5, 6), word("thought", 6, 7),
	}
	rows := []Row{{Speaker: 1, Start: sec(0), End: sec(7)}}
	turns := Attribute(spans, rows)
	if len(turns) != 1 {
		t.Fatalf("Attribute gave %d turns, want 1: %v", len(turns), turns)
	}
	if turns[0].Text != "first thought.\n\nsecond thought" {
		t.Errorf("text = %q, want a paragraph break at the pause", turns[0].Text)
	}
}

// A pause in the middle of a sentence is somebody thinking, and breaking there
// reads as a mistake in a way the wall of text does not.
func TestAttributeKeepsAnUnfinishedSentence(t *testing.T) {
	spans := []Span{
		word("the", 0, 1), word("movement", 1, 2),
		word("of", 5, 6), word("my", 6, 7),
	}
	rows := []Row{{Speaker: 1, Start: sec(0), End: sec(7)}}
	if got := Attribute(spans, rows); got[0].Text != "the movement of my" {
		t.Errorf("text = %q, want no break mid-sentence", got[0].Text)
	}
}

// Timestamps are off unless asked for: the document is what was said.
func TestRenderTimestamps(t *testing.T) {
	turns := []Turn{{Speaker: 1, Start: sec(3661), End: sec(3665), Text: "hello"}}
	if got := Render(turns, "talk", false); got != "# talk\n\n**S01**\n\nhello\n" {
		t.Errorf("Render = %q", got)
	}
	if got := Render(turns, "talk", true); got != "# talk\n\n**S01** [1:01:01]\n\nhello\n" {
		t.Errorf("Render with timestamps = %q", got)
	}
}

// Rows and words that do not overlap at all -- a diarizer that heard speech
// where the transcriber found none, or two runs over different audio -- leave
// one unattributed voice rather than a speaker numbered zero.
func TestAttributeWithoutOverlap(t *testing.T) {
	spans := []Span{word("hello", 0, 1), word("there", 1, 2)}
	rows := []Row{{Speaker: 2, Start: sec(60), End: sec(70)}}
	turns := Attribute(spans, rows)
	if len(turns) != 1 {
		t.Fatalf("Attribute = %v, want one turn", turns)
	}
	if turns[0].Speaker < 1 {
		t.Errorf("speaker is %d; numbering starts at one, and S00 reads as a bug", turns[0].Speaker)
	}
}

// Words grouped by speaker rather than laid out in time. A model that decodes
// one stream per speaker hands them back this way, and taking them at face
// value made a recording of two people taking turns read as one person talking
// for all of it and then the other.
func TestAttributeOrdersByTime(t *testing.T) {
	spans := []Span{
		// Everything the first speaker said, both stretches of it.
		{Start: 0, End: 900 * time.Millisecond, Text: "hello"},
		{Start: 4 * time.Second, End: 5 * time.Second, Text: "goodbye"},
		// Then everything the second one said, in between the two.
		{Start: 2 * time.Second, End: 3 * time.Second, Text: "hi"},
	}
	rows := []Row{
		{Speaker: 1, Start: 0, End: 1500 * time.Millisecond},
		{Speaker: 2, Start: 1800 * time.Millisecond, End: 3500 * time.Millisecond},
		{Speaker: 1, Start: 3800 * time.Millisecond, End: 5 * time.Second},
	}
	turns := Attribute(spans, rows)
	if len(turns) != 3 {
		t.Fatalf("%d turns, want three: %+v", len(turns), turns)
	}
	want := []struct {
		speaker int
		text    string
	}{{1, "hello"}, {2, "hi"}, {1, "goodbye"}}
	for i, w := range want {
		if turns[i].Speaker != w.speaker || turns[i].Text != w.text {
			t.Errorf("turn %d is S%d %q, want S%d %q", i, turns[i].Speaker, turns[i].Text, w.speaker, w.text)
		}
	}
	for i := 1; i < len(turns); i++ {
		if turns[i].Start < turns[i-1].Start {
			t.Errorf("turn %d starts before the one before it", i)
		}
	}
}

// A voice this model split across two of its streams is transcribed twice,
// and ordering those words by time interleaves the copies. Measured offsets
// between the two hearings were 80 to 240 ms.
func TestDedupeDropsTheSecondHearing(t *testing.T) {
	ms := time.Millisecond
	spans := []Span{
		{Start: 1000 * ms, End: 1080 * ms, Text: "not", Stream: 3},
		{Start: 1240 * ms, End: 1320 * ms, Text: "Not", Stream: 4},
		{Start: 2000 * ms, End: 2080 * ms, Text: "what", Stream: 3},
		{Start: 2080 * ms, End: 2160 * ms, Text: "what", Stream: 4},
		{Start: 2560 * ms, End: 2640 * ms, Text: "your", Stream: 4},
	}
	got := Dedupe(spans)
	if len(got) != 3 {
		t.Fatalf("kept %d of 5 spans: %+v", len(got), got)
	}
	for i, want := range []string{"not", "what", "your"} {
		if bare(got[i].Text) != want {
			t.Errorf("span %d is %q, want %q", i, got[i].Text, want)
		}
	}
}

// The same word twice from one stream is somebody saying it twice, which is
// what the timing rule alone would throw away: "very, very clear".
func TestDedupeKeepsARepetition(t *testing.T) {
	ms := time.Millisecond
	spans := []Span{
		{Start: 16000 * ms, End: 16240 * ms, Text: "very,", Stream: 1},
		{Start: 16240 * ms, End: 16480 * ms, Text: "very", Stream: 1},
		{Start: 16480 * ms, End: 16560 * ms, Text: "clear", Stream: 1},
	}
	if got := Dedupe(spans); len(got) != 3 {
		t.Errorf("kept %d of 3: %+v", len(got), got)
	}

	// And the same word said again later is not an echo of the first.
	far := []Span{
		{Start: 0, End: 80 * ms, Text: "Americans", Stream: 1},
		{Start: 13 * time.Second, End: 13*time.Second + 80*ms, Text: "Americans", Stream: 2},
	}
	if got := Dedupe(far); len(got) != 2 {
		t.Errorf("kept %d of 2 spans thirteen seconds apart: %+v", len(got), got)
	}
}
