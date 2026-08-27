package models

import "testing"

// The speaker cap is the column the menu exists for, so what it renders is
// worth pinning: an entry that declares no limit has not promised one, and
// printing "unlimited" there would be inventing a guarantee the model card
// never made.
func TestCapRendering(t *testing.T) {
	for _, c := range []struct {
		speakers int
		want     string
	}{
		{0, "unstated"},
		{4, "up to 4"},
	} {
		if got := (Pipeline{Speakers: c.speakers}).Cap(); got != c.want {
			t.Errorf("Cap(%d) = %q, want %q", c.speakers, got, c.want)
		}
	}
}

// A pair costs both halves to fetch. Someone reading the size column is
// deciding whether to download the entry, not one file in it.
func TestSizeCoversEveryHalf(t *testing.T) {
	p := Pipeline{Models: []Spec{{MiB: 1000}, {MiB: 24}}}
	if got, want := p.Size(), "1.0 GiB"; got != want {
		t.Errorf("Size() = %q, want %q", got, want)
	}
}

// An entry with a half missing cannot run, so it does not count as
// downloaded. This is the whole reason Downloaded is on the pipeline rather
// than read off its first model.
func TestDownloadedNeedsEveryHalf(t *testing.T) {
	p := Pipeline{Models: []Spec{{Name: "definitely-not-here", quant: "Q8_0"}}}
	if p.Downloaded() {
		t.Error("an entry whose model is not on disk reports downloaded")
	}
}

func TestLookupDiarizer(t *testing.T) {
	first := Diarizers[0]
	if got, ok := LookupDiarizer("1"); !ok || got.Name != first.Name {
		t.Errorf("LookupDiarizer(\"1\") = %q, %v; want %q", got.Name, ok, first.Name)
	}
	if got, ok := LookupDiarizer(first.Name); !ok || got.Name != first.Name {
		t.Errorf("LookupDiarizer(%q) = %q, %v", first.Name, got.Name, ok)
	}
	if _, ok := LookupDiarizer("0"); ok {
		t.Error("LookupDiarizer accepted a menu number below 1")
	}
	if _, ok := LookupDiarizer("nothing-of-the-sort"); ok {
		t.Error("LookupDiarizer accepted a name not in the menu")
	}
}

// Every entry has to name at least one model and say something about
// speakers, since an entry that cannot be run or cannot be compared is not
// worth a line in the menu.
func TestEveryEntryIsRunnable(t *testing.T) {
	for _, p := range Diarizers {
		if len(p.Models) == 0 {
			t.Errorf("%s: names no models", p.Name)
		}
		if p.Note == "" {
			t.Errorf("%s: no note saying what it is for", p.Name)
		}
		for _, s := range p.Models {
			if s.MiB == 0 {
				t.Errorf("%s: %s has no size, so the menu cannot say what fetching costs",
					p.Name, s.Name)
			}
		}
	}
}

// The detector is part of the entry, not a thing to remember separately: it
// counts towards the download, it has to be present before the entry can run,
// and DownloadPipeline has to fetch it.
func TestTheDetectorIsPartOfTheEntry(t *testing.T) {
	var withDetector *Pipeline
	for i := range Diarizers {
		if Diarizers[i].Speech.Name != "" {
			withDetector = &Diarizers[i]
			break
		}
	}
	if withDetector == nil {
		t.Fatal("no entry runs a voice activity detector")
	}

	all := withDetector.All()
	if len(all) != len(withDetector.Models)+1 {
		t.Errorf("All() returned %d models for an entry of %d plus a detector",
			len(all), len(withDetector.Models))
	}
	if all[len(all)-1].Name != withDetector.Speech.Name {
		t.Errorf("All() ends with %q, want the detector %q", all[len(all)-1].Name, withDetector.Speech.Name)
	}
	// And the entry's own Models are not disturbed by asking.
	if len(withDetector.Models) != len(withDetector.All())-1 {
		t.Error("All() appended to the entry's own slice")
	}

	sizeWith := withDetector.Size()
	bare := *withDetector
	bare.Speech = Spec{}
	if sizeWith == bare.Size() {
		t.Errorf("the detector costs nothing to download: %s either way", sizeWith)
	}
}

// A clustering diarizer is the one that can count past four, and it is also
// the one that cannot tell a voice from a chair by itself: loudness passes
// audible non-speech, which then clusters as a thing that is none of the
// voices and arrives as an extra speaker. An entry that counts must detect.
func TestCountingEntriesDetectSpeech(t *testing.T) {
	for _, p := range Diarizers {
		if p.Speakers > 4 && p.Speech.Name == "" {
			t.Errorf("%s counts up to %d speakers with no voice activity detector", p.Name, p.Speakers)
		}
	}
}
