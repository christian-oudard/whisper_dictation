// diarize answers who spoke when over a recording, which dictation never has
// to ask: one person holds the microphone and the daemon types what they said.
// A talk or an interview is the other shape, and the transcript is worthless
// without the speaker labels, so the question comes first here.
//
// It runs a diarizer, not a transcriber. sortformer produces no text at all,
// only speaker rows, which is why this is its own command rather than a flag
// on cmd/transcribe.
//
// Deliberately not a diktat subcommand, for the same reason cmd/transcribe is
// not: nothing here is part of dictating. The flake builds only cmd/diktat, so
// this is `go run ./cmd/diarize` inside the devShell.
//
//	go run ./cmd/diarize [-model <name>] [-preset <point>] [-o segments.tsv] file.wav
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	transcribe "github.com/handy-computer/transcribe.cpp/bindings/go"

	"github.com/christian-oudard/diktat/internal/asr"
	"github.com/christian-oudard/diktat/internal/human"
	"github.com/christian-oudard/diktat/internal/models"
	"github.com/christian-oudard/diktat/internal/wav"
)

// presets are sortformer's operating points. The default is whatever the GGUF
// shipped with; very-high is ~30s of lookahead and the point its published DER
// was measured at, which is the one to want on a file that is already on disk.
var presets = map[string]transcribe.SortformerPreset{
	"default":   transcribe.SortformerDefault,
	"very-high": transcribe.SortformerVeryHighLatency,
	"high":      transcribe.SortformerHighLatency,
	"low":       transcribe.SortformerLowLatency,
}

func main() {
	fs := flag.NewFlagSet("diarize", flag.ExitOnError)
	name := fs.String("model", "diar_streaming_sortformer_4spk-v2.1-F16.gguf", "diarizer to run")
	preset := fs.String("preset", "very-high", "operating point: default, very-high, high, low")
	out := fs.String("o", "", "write speaker rows to this TSV")
	fs.Parse(os.Args[1:])
	if fs.NArg() != 1 {
		log.Fatal("usage: diarize [-model <name>] [-preset <point>] [-o segments.tsv] file.wav")
	}

	point, ok := presets[*preset]
	if !ok {
		log.Fatalf("unknown preset %q", *preset)
	}

	samples, rate, err := wav.ReadWAV(fs.Arg(0))
	if err != nil {
		log.Fatalf("read audio: %v", err)
	}
	if rate != 16000 {
		log.Fatalf("%s is %d Hz; the models take 16 kHz mono", fs.Arg(0), rate)
	}
	clip := time.Duration(len(samples)) * time.Second / time.Duration(rate)

	modelPath := models.Resolve(*name)
	if err := models.Check(modelPath); err != nil {
		log.Fatalf("%v", err)
	}
	model, err := asr.Load(modelPath)
	if err != nil {
		log.Fatalf("load model: %v", err)
	}
	defer model.Close()
	if !model.Supports(transcribe.FeatureDiarization) {
		log.Fatalf("%s does not diarize", model.Name())
	}
	fmt.Printf("%s, %s resident\n", model.Arch(), human.Bytes(model.Bytes()))

	// Punctuation asked for rather than left to the family default, which for
	// the multitalker parakeet is off: the first run of the whole recording
	// came back as 109 minutes of unbroken lowercase, which is not a
	// transcript anyone can read.
	//
	// Timestamps stay on Auto, which is the finest the model has and is never
	// rejected. Asking for words outright is: a diarizer produces no text to
	// hang them on, and sortformer fails the run rather than ignoring it.
	opts := &transcribe.RunOptions{Diarize: transcribe.ModeOn, PNC: transcribe.ModeOn}
	// The preset rides the run slot even though it is a stream setting, since
	// sortformer streams inside one run. A model that takes no such extension
	// fails the run rather than ignoring it, so ask before setting it.
	if model.AcceptsExtension(transcribe.SlotRun, transcribe.KindSortformerStream) {
		opts.Family = &transcribe.SortformerStreamOptions{Preset: transcribe.Opt(point)}
	}

	fmt.Printf("diarizing %s...\n", clip.Round(time.Second))
	t0 := time.Now()
	res, err := model.Run(context.Background(), samples, opts)
	if err != nil {
		log.Fatalf("diarize: %v", err)
	}
	elapsed := time.Since(t0)
	fmt.Printf("%s, %sx realtime\n\n", elapsed.Round(time.Millisecond),
		speedup(clip, elapsed))

	// A joint model transcribes and attributes in one pass, so its segments
	// already carry a speaker. Printing them is what says whether the
	// attribution is any good: talk time alone cannot tell a real third
	// speaker from a clustering artefact, and a line of dialogue can.
	for _, s := range res.Segments {
		if s.Speaker > 0 {
			fmt.Printf("[%s] S%02d: %s\n", s.Start.Round(time.Second), s.Speaker, s.Text)
		}
	}

	rows := res.SpeakerSegments
	if len(rows) == 0 {
		log.Fatal("no speaker rows: the model heard nobody")
	}
	report(rows, clip)

	if *out != "" {
		if err := writeTSV(*out, rows); err != nil {
			log.Fatalf("write rows: %v", err)
		}
		fmt.Printf("\n%d rows written to %s\n", len(rows), *out)
	}
}

// speedup is how much faster than the audio the run went. Whole numbers past 1x
// and a decimal below it: the same model runs at 269x on a card and 0.3x on
// this CPU, and rounding the second to "0x" loses the only number that says
// whether a run is worth starting.
func speedup(clip, elapsed time.Duration) string {
	x := clip.Seconds() / elapsed.Seconds()
	if x < 10 {
		return fmt.Sprintf("%.1f", x)
	}
	return fmt.Sprintf("%.0f", x)
}

// report says how the recording divides. Talk time is the first thing to look
// at: a talk with questions from the floor and a two-way interview both come
// back as several speakers, and only the shares tell them apart.
func report(rows []transcribe.SpeakerSegment, clip time.Duration) {
	talk := map[int]time.Duration{}
	turns := map[int]int{}
	for _, r := range rows {
		talk[r.Speaker] += r.End - r.Start
		turns[r.Speaker]++
	}
	ids := make([]int, 0, len(talk))
	for id := range talk {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return talk[ids[i]] > talk[ids[j]] })

	fmt.Printf("%d speakers in %s\n", len(ids), clip.Round(time.Second))
	for _, id := range ids {
		fmt.Printf("  S%02d  %8s  %4.1f%%  %d turns\n", id,
			talk[id].Round(time.Second), 100*talk[id].Seconds()/clip.Seconds(), turns[id])
	}
	// Speech and overlap are what say whether the rows are plausible at all.
	// A recording that is 30% speech is mostly music or silence, and one with
	// heavy overlap is a crowd, which is where every diarizer struggles.
	fmt.Printf("speech %s (%.0f%%), overlap %s (%.0f%%)\n",
		speech(rows).Round(time.Second), 100*speech(rows).Seconds()/clip.Seconds(),
		overlap(rows).Round(time.Second), 100*overlap(rows).Seconds()/clip.Seconds())
}

// speech is the union of the rows, counting a moment once however many people
// were talking through it.
func speech(rows []transcribe.SpeakerSegment) time.Duration {
	return covered(rows, 1)
}

// overlap is where at least two rows agree, which is the part of the audio no
// single-stream transcript can attribute to both.
func overlap(rows []transcribe.SpeakerSegment) time.Duration {
	return covered(rows, 2)
}

// covered is the time at least n rows cover at once, by sweeping the row
// starts and ends in order and counting how many are open.
func covered(rows []transcribe.SpeakerSegment, n int) time.Duration {
	type edge struct {
		at   time.Duration
		open int
	}
	edges := make([]edge, 0, 2*len(rows))
	for _, r := range rows {
		edges = append(edges, edge{r.Start, 1}, edge{r.End, -1})
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].at != edges[j].at {
			return edges[i].at < edges[j].at
		}
		return edges[i].open > edges[j].open
	})
	var total time.Duration
	var depth int
	var since time.Duration
	for _, e := range edges {
		if depth >= n {
			total += e.at - since
		}
		depth += e.open
		if depth >= n {
			since = e.at
		}
	}
	return total
}

// writeTSV is the handoff to whatever attributes text to these rows. Seconds
// rather than durations, since that is what the timestamps on a transcript are
// in and the join is between the two.
func writeTSV(path string, rows []transcribe.SpeakerSegment) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, r := range rows {
		fmt.Fprintf(w, "S%02d\t%.2f\t%.2f\n", r.Speaker, r.Start.Seconds(), r.End.Seconds())
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Close()
}
