package transcript

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Speakers is how many distinct people the turns name.
func Speakers(turns []Turn) int {
	seen := map[int]bool{}
	for _, t := range turns {
		seen[t.Speaker] = true
	}
	return len(seen)
}

// Render writes the document. Markdown rather than plain text because the thing
// a reader does with a transcript is search it and quote from it, and both want
// the speaker on its own line rather than buried in a prefix.
//
// Consecutive turns by one person are joined into a paragraph: the model breaks
// on its own rhythm, which is roughly a sentence, and a transcript with one line
// per sentence reads as a list rather than as speech.
//
// The document is what was said and nothing else. How long the recording was,
// which pipeline ran and how the talk time divided are all facts about the run
// rather than about the conversation, so they go to the log, where they are
// read once and do not have to be deleted out of the transcript afterwards.
//
// Timestamps are off unless asked for. They are worth having when the document
// is an index into the audio, and in the way when it is something to read.
func Render(turns []Turn, title string, stamps bool) string {
	// Built as blocks and joined, rather than written straight out: every block
	// here is separated from the next by a blank line, and a heading that lost
	// one is not a heading, it is the last line of the paragraph above it.
	blocks := []string{"# " + title}
	speaker, end, said := -1, time.Duration(0), ""
	for _, t := range turns {
		switch {
		case t.Speaker != speaker:
			speaker = t.Speaker
			name := fmt.Sprintf("**S%02d**", t.Speaker)
			if stamps {
				name += " [" + Stamp(t.Start) + "]"
			}
			blocks = append(blocks, name, t.Text)
		case Break(said, t.Start-end):
			// The same person after a pause: a paragraph, for the same reason
			// the pauses inside a turn are.
			blocks = append(blocks, t.Text)
		default:
			blocks[len(blocks)-1] += " " + t.Text
		}
		end, said = t.End, t.Text
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// Shares is the talk-time table, for the log. It is what says whether the
// labels are plausible before a word of the transcript is read: a workshop
// where four people each hold a quarter of the time is a workshop that has been
// mislabelled.
func Shares(turns []Turn, clip time.Duration) string {
	talk := map[int]time.Duration{}
	count := map[int]int{}
	for _, t := range turns {
		talk[t.Speaker] += t.End - t.Start
		count[t.Speaker]++
	}
	ids := make([]int, 0, len(talk))
	for id := range talk {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return talk[ids[i]] > talk[ids[j]] })

	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "  S%02d  %8s  %4.0f%%  %d turns\n", id,
			talk[id].Round(time.Second), 100*talk[id].Seconds()/clip.Seconds(), count[id])
	}
	return b.String()
}

// Stamp is h:mm:ss, since a recording long enough to need cutting is long
// enough for minutes alone to be useless for finding a moment in the audio.
func Stamp(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}
