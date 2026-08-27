package models

import "testing"

// The catalog claims a language set per model so the menu can answer before
// anything is downloaded. asr.TestLangsMatchLibrary checks those claims
// against the library; this only checks the rendering, which the menu depends
// on for its column width.
func TestLanguagesRendering(t *testing.T) {
	for _, c := range []struct {
		langs []string
		want  string
	}{
		{nil, "Worldwide"},
		{[]string{}, "Worldwide"},
		{[]string{"en"}, "English"},
		// Two is short enough to name outright, which says more than the
		// reach of the set does at that size.
		{[]string{"en", "zh"}, "English, Chinese"},
		{[]string{"en", "de"}, "English, de"},
		{[]string{"en", "de", "es", "fr"}, "European (4)"},
		{[]string{"en", "de", "es", "fr", "pt"}, "European (5)"},
		// One language from outside Europe is enough: the point of the label
		// is that nothing has been left out of it.
		{[]string{"en", "de", "ja"}, "Worldwide (3)"},
	} {
		if got := (Spec{Langs: c.langs}).Languages(); got != c.want {
			t.Errorf("Languages(%v) = %q, want %q", c.langs, got, c.want)
		}
	}
	// The column is padded to 14; anything longer would push the state column
	// out of line for every row.
	for _, s := range Catalog {
		if got := s.Languages(); len(got) > 14 {
			t.Errorf("%s: language column %q is %d wide, over the 14 the menu pads to",
				s.Name, got, len(got))
		}
	}
}
