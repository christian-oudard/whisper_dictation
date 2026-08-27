package main

import (
	"testing"
	"time"

	transcribe "github.com/handy-computer/transcribe.cpp/bindings/go"

	"github.com/christian-oudard/diktat/internal/models"
	"github.com/christian-oudard/diktat/internal/silence"
)

// One run of a model that decodes a stream per speaker: two segments, the
// words of each, and the diarizer's own rows.
func twoStreams() transcribe.Result {
	s := time.Second
	return transcribe.Result{
		Segments: []transcribe.Segment{
			{Start: 0, End: 9 * s, Speaker: 1, Text: "hello there goodbye"},
			{Start: 4 * s, End: 6 * s, Speaker: 2, Text: "hi"},
		},
		Words: []transcribe.Word{
			{Start: 0, End: s, Segment: 0, Text: "hello"},
			{Start: 2 * s, End: 3 * s, Segment: 0, Text: "there"},
			{Start: 8 * s, End: 9 * s, Segment: 0, Text: "goodbye"},
			{Start: 4 * s, End: 5 * s, Segment: 1, Text: "hi"},
		},
		SpeakerSegments: []transcribe.SpeakerSegment{
			{Start: 0, End: 3500 * time.Millisecond, Speaker: 1},
			{Start: 3800 * time.Millisecond, End: 6 * s, Speaker: 2},
			{Start: 7 * s, End: 9 * s, Speaker: 1},
		},
	}
}

// The model's own segments say one thing per speaker and span everything that
// speaker said, so taken at face value a conversation reads as one person
// talking throughout and then the other. Its words and the diarizer's rows say
// better, and both are in the same result.
func TestTurnsInOrdersByTime(t *testing.T) {
	turns := turnsIn(twoStreams(), 0, silence.Timeline{})
	if len(turns) != 3 {
		t.Fatalf("%d turns, want three: %+v", len(turns), turns)
	}
	for i, want := range []struct {
		speaker int
		text    string
	}{{1, "hello there"}, {2, "hi"}, {1, "goodbye"}} {
		if turns[i].Speaker != want.speaker || turns[i].Text != want.text {
			t.Errorf("turn %d is S%d %q, want S%d %q", i, turns[i].Speaker, turns[i].Text,
				want.speaker, want.text)
		}
	}
}

// An entry with no detector runs none: speechIn is reached for every pipeline
// and most of them have nothing to run, so it must answer without loading
// anything. A model load here would be a fatal error on a machine with no
// model downloaded, which is where the tests run.
func TestNoDetectorRunsNothing(t *testing.T) {
	p := models.Pipeline{Name: "words only", Models: []models.Spec{{Name: "whatever"}}}
	if speech := speechIn(p, make([]float32, 16000), time.Second); speech != nil {
		t.Errorf("an entry with no detector produced %d speech regions", len(speech))
	}
}

// A model that attributes its own speakers cannot carry the numbering across a
// cut, so an entry built on one refuses a recording it cannot hold and names
// the entry that can. The name it gives is the one that transcribes with the
// same model and takes its speakers from a diarizer, where such an entry
// exists, since that is the smallest change from what was asked for.
func TestPairedWithNamesTheEntryThatWorks(t *testing.T) {
	for _, p := range models.Diarizers {
		if len(p.Models) != 1 {
			continue
		}
		name := pairedWith(p)
		if name == p.Name {
			t.Errorf("%s is offered as its own repair", p.Name)
		}
		other, ok := models.LookupDiarizer(name)
		if !ok {
			t.Fatalf("%s points at %q, which is not in the menu", p.Name, name)
		}
		if len(other.Models) < 2 {
			t.Errorf("%s points at %q, which has no diarizer either", p.Name, name)
		}
		// Where a paired variant of the same words model exists, that is the
		// one to name.
		if other.Models[0].Name != p.Models[0].Name {
			for _, cand := range models.Diarizers {
				if len(cand.Models) == 2 && cand.Models[0].Name == p.Models[0].Name {
					t.Errorf("%s points at %q, but %q pairs the same words model",
						p.Name, name, cand.Name)
				}
			}
		}
	}
}
