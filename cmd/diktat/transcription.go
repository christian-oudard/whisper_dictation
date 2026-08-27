package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	transcribe "github.com/handy-computer/transcribe.cpp/bindings/go"

	"github.com/christian-oudard/diktat/internal/asr"
	"github.com/christian-oudard/diktat/internal/config"
	"github.com/christian-oudard/diktat/internal/human"
	"github.com/christian-oudard/diktat/internal/models"
	"github.com/christian-oudard/diktat/internal/transcript"
	"github.com/christian-oudard/diktat/internal/wav"
)

// runTranscribe turns a recording into a document that says who spoke.
//
// Not the daemon's job and not dictation: this runs once over a file and exits,
// holding a model far too large to sit beside a desktop and taking minutes
// rather than the moment a keypress can wait for.
//
//	diktat transcribe [-timestamps] [-o out.md] recording.opus
func runTranscribe(args []string) {
	log.SetFlags(0)
	fs := flag.NewFlagSet("transcribe", flag.ExitOnError)
	out := fs.String("o", "", "write the transcript here (default: alongside the audio)")
	chunk := fs.Duration("chunk", 0, "longest piece to transcribe at once (default: what the pipeline can hold)")
	name := fs.String("model", "", "`pipeline` to use (default: the one \"diktat tx-model\" chose)")
	lang := fs.String("lang", "", "`language` of the recording, \"list\" for the ones it takes, or empty to let the model detect it")
	stamps := fs.Bool("timestamps", false, "put the time of each turn in the document")
	diarizer := fs.String("diarizer", "", "`GGUF` to attribute speakers with, instead of the pipeline's")
	// How many people are in the recording, when that is known. A clustering
	// diarizer estimates it and the estimate is a real estimate: it cannot
	// tell one person recorded two ways from two people, and on a recording
	// of three voices it has split one of them in two. Somebody who was in
	// the room knows better and this is how they say so.
	var speakers int
	fs.IntVar(&speakers, "speakers", 0, "how many people are in the recording, if you know")
	fs.IntVar(&speakers, "s", 0, "shorthand for -speakers")
	fs.Parse(args)
	if *lang == "list" {
		listLanguages()
		return
	}
	if fs.NArg() != 1 {
		log.Fatal("usage: diktat transcribe [-timestamps] [-o out.md] [-model <pipeline>] recording")
	}

	// The turns file that came out of an earlier run, rendered again. This is
	// what the sidecar is for: an hour of audio costs minutes on a card and a
	// change of mind about the document costs none of it.
	if filepath.Ext(fs.Arg(0)) == ".json" {
		rerender(fs.Arg(0), *out, *stamps)
		return
	}

	choice := *name
	if choice == "" {
		choice = config.TxModel()
	}
	p, ok := models.LookupDiarizer(choice)
	if !ok {
		log.Fatalf("unknown pipeline %q", choice)
	}
	if !p.Downloaded() {
		log.Fatalf("%s is not downloaded. Get it with:\n  diktat tx-model %s", p.Name, p.Name)
	}
	// Before the audio is read rather than after it is transcribed: a hint the
	// model does not take is either ignored or fatal depending on the family,
	// and an hour of work either way is too late to say so.
	if !p.Takes(*lang) {
		log.Fatalf("%s does not take %s. It takes: %s", p.Name, *lang, p.Codes())
	}

	samples, rate, err := wav.Read(fs.Arg(0))
	if err != nil {
		log.Fatalf("read audio: %v", err)
	}
	if rate != transcript.SampleRate {
		log.Fatalf("%s is %d Hz; the models take 16 kHz mono", fs.Arg(0), rate)
	}
	clip := time.Duration(len(samples)) * time.Second / transcript.SampleRate
	log.Printf("%s of audio, %s", clip.Round(time.Second), p.Name)

	turns := run(p, samples, clip, *chunk, *lang, *diarizer, speakers)
	// The entry's speaker cap belongs to the diarizer it names, so it says
	// nothing about a run that was given a different one.
	cap := p.Speakers
	if *diarizer != "" {
		cap = 0
	}
	if len(turns) == 0 {
		log.Fatal("nothing was transcribed")
	}

	path := *out
	if path == "" {
		path = strings.TrimSuffix(fs.Arg(0), filepath.Ext(fs.Arg(0))) + ".md"
	}
	// The turns beside the document, because rendering is free and transcribing
	// is not: a change of mind about how a transcript should look must not cost
	// another run over the audio.
	if err := writeTurns(strings.TrimSuffix(path, filepath.Ext(path))+".json", turns); err != nil {
		log.Printf("could not write the turns: %v", err)
	}
	title := strings.TrimSuffix(filepath.Base(fs.Arg(0)), filepath.Ext(fs.Arg(0)))
	doc := transcript.Render(turns, title, *stamps)
	if err := os.WriteFile(path, []byte(doc), 0644); err != nil {
		log.Fatalf("write transcript: %v", err)
	}
	found := transcript.Speakers(turns)
	log.Printf("%d turns, %d speakers, written to %s", len(turns), found, path)
	log.Print(transcript.Shares(turns, clip))
	// A count that reaches the cap is a floor, not a count, and nothing further
	// down the line can tell the difference: four labels on a recording of a
	// dozen people look exactly like four people. Said here because this is the
	// only place that knows both numbers.
	if cap > 0 && found >= cap {
		log.Printf("%d is the most %s can tell apart, so it is what this recording"+
			" has at least, not what it has: any voice past that is pooled"+
			" into one of these labels", cap, p.Name)
	}
}

// listLanguages says what every entry takes, which the menu's column cannot:
// it names the reach of a set of 25 rather than its members, and `-lang` is
// given a member.
func listLanguages() {
	for _, p := range models.Diarizers {
		fmt.Printf("%s\n  %s\n", p.Name, p.Codes())
	}
}

// run transcribes the whole recording with one pipeline and returns its turns.
//
// Two shapes, and the difference is where the speaker identity comes from. An
// entry with a diarizer in it gets identity from that diarizer, run over the
// whole recording in one pass, and the words from a model cut into pieces; an
// entry that transcribes and attributes in the same pass has to carry its own
// identity across the cuts, which is much weaker and is why the pairs exist.
//
// The models load one at a time and the diarizer is closed before the words
// model opens, since the card holds one of these at once and neither needs the
// other resident: the speaker rows are the only thing that crosses.
func run(p models.Pipeline, samples []float32, clip, chunk time.Duration, language, diarizer string, speakers int) []transcript.Turn {
	// A diarizer named outright replaces whatever the entry carries, and gives
	// one to an entry that has none. The menu is a set of pairings known to
	// work; this is for a model that is not in it yet, which is every model the
	// week it is published.
	if diarizer != "" {
		rows := speakerRows(diarizer, samples, clip, speechIn(p, samples, clip), speakers)
		model := load(p.Models[0])
		defer model.Close()
		spans := spansOf(model, samples, piece(p, model, clip, chunk), language)
		return transcript.Attribute(spans, rows)
	}
	if len(p.Models) == 2 {
		rows := speakerRows(p.Models[1].Path(), samples, clip, speechIn(p, samples, clip), speakers)
		model := load(p.Models[0])
		defer model.Close()
		spans := spansOf(model, samples, piece(p, model, clip, chunk), language)
		return transcript.Attribute(spans, rows)
	}
	model := load(p.Models[0])
	defer model.Close()
	if !model.Supports(transcribe.FeatureDiarization) {
		log.Fatalf("%s does not attribute speech to speakers, and this pipeline has no diarizer to do it", model.Name())
	}
	// A model that attributes speech itself was trained around a fixed number
	// of speakers, so there is nothing to tell. Said rather than ignored: a
	// count that quietly does nothing is worse than one that is refused, since
	// the transcript that comes back looks like it was taken into account.
	if speakers > 0 {
		log.Fatalf("%s decides its own speakers and cannot be told how many;"+
			" pick an entry with a clustering diarizer, such as %q", p.Name, models.Diarizers[0].Name)
	}
	return transcribeWhole(p, model, samples, piece(p, model, clip, chunk), language)
}

// load opens one of a pipeline's models and says what it cost.
func load(spec models.Spec) *asr.Model { return loadPath(spec.Path()) }

// loadPath opens a model by path, which is what a diarizer named on the
// command line is.
func loadPath(path string) *asr.Model {
	model, err := asr.Load(path)
	if err != nil {
		log.Fatalf("load model: %v", err)
	}
	log.Printf("%s, %s resident", model.Arch(), human.Bytes(model.Bytes()))
	return model
}

// piece is how much audio to hand the words model at once: what the menu entry
// declares, what the model declares, or the whole recording, and -chunk over
// all of them.
//
// Not asr.AudioLimit. That is the daemon's bound on a single utterance, and a
// model that has run nothing gets a 30 second floor by design, which here cuts
// an hour into two hundred pieces.
func piece(p models.Pipeline, model *asr.Model, clip, chunk time.Duration) time.Duration {
	limit := p.Piece
	if chunk != 0 {
		limit = chunk
	}
	if limit == 0 {
		limit = clip
	}
	if declared := model.MaxAudio(); declared > 0 && declared < limit {
		limit = declared
	}
	return limit
}

// speakerRows runs the diarizer over the whole recording and returns who spoke
// when. One pass over everything, which is what makes the labels worth having:
// sortformer streams with a speaker cache and took 1h49m of workshop in 248
// MiB, so there is nothing to cut and no numbering to reconcile.
// minSpeech is the shortest stretch worth calling speech. See speechIn.
const minSpeech = 500 * time.Millisecond

// speechIn is where the speech is, from the entry's detector, or nil for an
// entry without one.
//
// A clustering diarizer needs this and a fixed-cap one does not: loudness
// cannot tell a voice from a chair, and a chair that passes a loudness gate
// clusters as a thing that is none of the voices, which arrives as an extra
// speaker. Measured on four AMI meetings, the regions improve speaker
// confusion on every one and fix the count on two of the three that were over.
//
// The detector is closed before anything else opens. It is 2 MiB and a few
// seconds either way, and holding it while a 700 MiB model loads is a card's
// worth of nothing.
func speechIn(p models.Pipeline, samples []float32, clip time.Duration) []transcribe.Span {
	if p.Speech.Name == "" {
		return nil
	}
	model := loadPath(p.Speech.Path())
	defer model.Close()

	t0 := time.Now()
	res, err := model.Run(context.Background(), samples, &transcribe.RunOptions{Diarize: transcribe.ModeOn})
	if err != nil {
		log.Fatalf("detect speech: %v", err)
	}
	var speech []transcribe.Span
	var total, dropped time.Duration
	for _, r := range res.SpeakerSegments {
		// Too short to be anybody saying anything. The detector answers
		// whether a frame is speech, and a cough, a door or a laugh gets a
		// yes; what tells those from a word is that nobody says a word in
		// under half a second, not even "yes". They matter here more than
		// they do to the detector, because each one becomes an embedding
		// window that is mostly not speech, and those cluster together as a
		// thing that is none of the voices.
		//
		// Measured on four AMI meetings, dropping them takes the last
		// over-count out -- IS1008a goes from five speakers to four -- and
		// improves confusion on three of the four. A second at the same
		// threshold does the same, so this is a floor rather than a fitted
		// constant.
		if r.End-r.Start < minSpeech {
			dropped += r.End - r.Start
			continue
		}
		speech = append(speech, transcribe.Span{Start: r.Start, End: r.End})
		total += r.End - r.Start
	}
	log.Printf("  %s of speech in %s, detected in %s (%s too short to be a word)",
		total.Round(time.Second), clip.Round(time.Second),
		time.Since(t0).Round(time.Second), dropped.Round(time.Second))
	return speech
}

func speakerRows(path string, samples []float32, clip time.Duration, speech []transcribe.Span, speakers int) []transcript.Row {
	model := loadPath(path)
	defer model.Close()
	if !model.Supports(transcribe.FeatureDiarization) {
		log.Fatalf("%s does not diarize", model.Name())
	}

	opts := &transcribe.RunOptions{Diarize: transcribe.ModeOn}
	// The published DER is measured at the longest lookahead, which is the one
	// to want on a file that is already on disk: nothing here is waiting for the
	// answer in real time. A model that takes no such extension fails the run
	// rather than ignoring it, so ask before setting it.
	if model.AcceptsExtension(transcribe.SlotRun, transcribe.KindSortformerStream) {
		opts.Family = &transcribe.SortformerStreamOptions{
			Preset: transcribe.Opt(transcribe.SortformerVeryHighLatency)}
	}
	// The clustering diarizer, which is the one that takes the speech regions
	// and the one whose speaker count is an answer rather than a constant.
	if model.AcceptsExtension(transcribe.SlotRun, transcribe.KindTitanetDiarize) {
		ext := &transcribe.TitanetDiarizeOptions{Speech: speech}
		if speakers > 0 {
			ext.Speakers = &speakers
		}
		opts.Family = ext
	} else if speakers > 0 {
		log.Fatalf("%s attributes a fixed number of speakers and cannot be told how many", model.Name())
	}
	t0 := time.Now()
	res, err := model.Run(context.Background(), samples, opts)
	if err != nil {
		log.Fatalf("diarize: %v", err)
	}
	if len(res.SpeakerSegments) == 0 {
		log.Fatal("no speaker rows: the diarizer heard nobody")
	}
	rows := make([]transcript.Row, 0, len(res.SpeakerSegments))
	seen := map[int]bool{}
	for _, r := range res.SpeakerSegments {
		rows = append(rows, transcript.Row{Speaker: r.Speaker, Start: r.Start, End: r.End})
		seen[r.Speaker] = true
	}
	log.Printf("  %d speakers in %s, diarized in %s", len(seen), clip.Round(time.Second),
		time.Since(t0).Round(time.Second))
	return rows
}

// spansOf transcribes the recording in pieces and returns the text with the
// audio behind each bit of it, in recording time.
//
// Word spans where the model stamps words, whole segments where it does not.
// The difference is how precisely a change of speaker can be placed, and it is
// the reason `cmd/caps` exists: of the models here only the parakeets stamp per
// token.
func spansOf(model *asr.Model, samples []float32, limit time.Duration, language string) []transcript.Span {
	opts := &transcribe.RunOptions{PNC: transcribe.ModeOn, Language: language}
	if model.MaxTimestamps() == transcribe.StampsWord {
		opts.Timestamps = transcribe.StampsWord
	}

	var spans []transcript.Span
	for _, r := range pieces(samples, limit) {
		spans = append(spans, spansIn(transcribeAt(model, samples[r.from:r.to], r.at, opts), r.at)...)
	}
	return spans
}

// spansIn is one run's text, as finely as it stamped: words where it has them,
// segments where it stops there. at is where this piece starts in the
// recording, since a model is handed one piece and stamps from zero.
func spansIn(res transcribe.Result, at time.Duration) []transcript.Span {
	var spans []transcript.Span
	if len(res.Words) > 0 {
		for _, w := range res.Words {
			if text := strings.TrimSpace(w.Text); text != "" {
				spans = append(spans, transcript.Span{Start: at + w.Start, End: at + w.End,
					Text: text, Stream: stream(res, w.Segment)})
			}
		}
		return spans
	}
	for i, s := range res.Segments {
		if text := strings.TrimSpace(s.Text); text != "" {
			spans = append(spans, transcript.Span{Start: at + s.Start, End: at + s.End,
				Text: text, Stream: stream(res, i)})
		}
	}
	return spans
}

// stream is which of a model's per-speaker decodes a segment is, or 0 when it
// decoded the audio once. A word belongs to the segment that produced it, and
// that segment's speaker is the stream it was heard on.
func stream(res transcribe.Result, seg int) int {
	if seg < 0 || seg >= len(res.Segments) {
		return 0
	}
	return res.Segments[seg].Speaker
}

// part is one piece of the recording: sample range, and where it starts in
// recording time.
type part struct {
	from, to int
	at       time.Duration
}

// pieces cuts the recording into runnable lengths, each cut in the quietest
// moment near it. The search window is a few seconds: long enough to find the
// gap between two sentences, short enough that the pieces stay the length the
// memory bound asked for.
func pieces(samples []float32, limit time.Duration) []part {
	cuts := transcript.Cuts(samples, limit, 10*time.Second)
	if len(cuts) > 1 {
		log.Printf("cutting into %d pieces of up to %s, in the quietest moment near each cut",
			len(cuts), limit.Round(time.Second))
	}
	out := make([]part, len(cuts))
	for i, cut := range cuts {
		to := len(samples)
		if i+1 < len(cuts) {
			to = cuts[i+1]
		}
		out[i] = part{cut, to, time.Duration(cut) * time.Second / transcript.SampleRate}
	}
	return out
}

// transcribeAt runs one piece and says how it went.
func transcribeAt(model *asr.Model, samples []float32, at time.Duration, opts *transcribe.RunOptions) transcribe.Result {
	length := time.Duration(len(samples)) * time.Second / transcript.SampleRate
	t0 := time.Now()
	res, err := model.Run(context.Background(), samples, opts)
	if err != nil {
		log.Fatalf("transcribe %s at %s: %v", length.Round(time.Second),
			at.Round(time.Second), err)
	}
	log.Printf("  %s at %s in %s", length.Round(time.Second), at.Round(time.Second),
		time.Since(t0).Round(time.Second))
	return res
}

// transcribeWhole runs a model that transcribes and attributes speakers in the
// same pass, over the whole recording at once.
//
// At once, and not in pieces, because such a model numbers speakers in order
// of first appearance within whatever audio it is handed and has no memory of
// the piece before. Nothing downstream can undo that: two pieces both open on
// speaker 1 and there is no way to know from the text whether that is one
// person or two.
//
// It used to be reconciled by repeating thirty seconds of the previous piece
// and matching the numbering over the stretch both transcribed. That works
// only for people who spoke on both sides of a cut, which for a dozen voices
// fails often enough to be worthless -- one voice heard in a single piece
// became a new speaker, and 12 people came back as 71. The repair is not a
// better text matcher; it is a diarizer that sees the whole recording, which
// is what every paired entry in the menu already does. So this refuses rather
// than doing it badly, and names the entry that does it properly.
func transcribeWhole(p models.Pipeline, model *asr.Model, samples []float32, limit time.Duration,
	language string) []transcript.Turn {
	if cuts := pieces(samples, limit); len(cuts) > 1 {
		log.Fatalf("%s attributes speakers itself and numbers them from one in every piece,"+
			" so a recording it cannot hold in one pass comes back with the same person"+
			" under several numbers.\nThis one needs %d pieces of %s. Use %q, whose diarizer"+
			" sees the whole recording.",
			p.Name, len(cuts), limit.Round(time.Second), pairedWith(p))
	}

	opts := &transcribe.RunOptions{Diarize: transcribe.ModeOn, PNC: transcribe.ModeOn, Language: language}
	// Asked for rather than left on Auto, because the words are what the join
	// below needs and Auto is per family. Only where the model says it stamps
	// them: a model asked for stamps it does not have fails the run.
	if model.MaxTimestamps() == transcribe.StampsWord {
		opts.Timestamps = transcribe.StampsWord
	}
	return turnsIn(transcribeAt(model, samples, 0, opts), 0, 0)
}

// pairedWith is the menu entry that transcribes with the same model and takes
// its speakers from a diarizer instead, or the first entry that clusters when
// there is no such pair.
func pairedWith(p models.Pipeline) string {
	for _, other := range models.Diarizers {
		if len(other.Models) == 2 && len(p.Models) > 0 && other.Models[0].Name == p.Models[0].Name {
			return other.Name
		}
	}
	return models.Diarizers[0].Name
}

// turnsIn is who said what in one run of a model that attributes speech
// itself.
//
// Its own segments are the obvious answer and the wrong one. This family
// decodes one stream per speaker and returns a segment per stream, so a
// recording where two people take turns comes back as one segment each,
// covering everything from that speaker's first word to their last. Rendered
// in order that reads as one person talking for the whole recording and then
// the other, which is not what happened.
//
// The pieces to do better are in the same result: the words are stamped, and
// the diarizer's own rows say who was speaking when, in recording time and
// independently of the transcript. Joining those two is exactly what a
// pipeline with a separate diarizer does, so it is the same join here.
//
// A model that gives one but not the other keeps its segments, which is the
// best that can be done with them.
func turnsIn(res transcribe.Result, at, from time.Duration) []transcript.Turn {
	spans := spansIn(res, at)
	if from > 0 {
		// The lead-in, which the piece before this one already transcribed.
		// Dropped word by word rather than turn by turn: a turn that starts in
		// the lead-in and runs past it is one somebody went on talking
		// through, and dropping the whole of it loses everything they said
		// after the boundary. On a three minute piece that is however long
		// they held the floor.
		kept := spans[:0:0]
		for _, s := range spans {
			if s.Start >= from {
				kept = append(kept, s)
			}
		}
		spans = kept
	}
	var rows []transcript.Row
	for _, r := range res.SpeakerSegments {
		rows = append(rows, transcript.Row{Speaker: r.Speaker, Start: at + r.Start, End: at + r.End})
	}
	if len(spans) > 0 && len(rows) > 0 {
		// Deduped first: separating speakers by masking the audio leaks, so a
		// voice this model split across two of its streams is transcribed
		// twice, and ordering those words by time interleaves the copies.
		return transcript.Attribute(transcript.Dedupe(spans), rows)
	}
	var turns []transcript.Turn
	for _, s := range res.Segments {
		if s.Speaker <= 0 || strings.TrimSpace(s.Text) == "" {
			continue
		}
		turns = append(turns, transcript.Turn{Speaker: s.Speaker,
			Start: at + s.Start, End: at + s.End, Text: strings.TrimSpace(s.Text)})
	}
	return turns
}

// rerender writes the document again from a saved set of turns.
func rerender(path, out string, stamps bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read turns: %v", err)
	}
	var turns []transcript.Turn
	if err := json.Unmarshal(data, &turns); err != nil {
		log.Fatalf("%s: %v", path, err)
	}
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if out == "" {
		out = strings.TrimSuffix(path, filepath.Ext(path)) + ".md"
	}
	if err := os.WriteFile(out, []byte(transcript.Render(turns, title, stamps)), 0644); err != nil {
		log.Fatalf("write transcript: %v", err)
	}
	log.Printf("%d turns, %d speakers, written to %s", len(turns), transcript.Speakers(turns), out)
}

// writeTurns saves the turns as JSON, which is the artifact the document is
// rendered from. Keeping it means a relabelling, or a different format, costs
// nothing: the expensive half already happened.
func writeTurns(path string, turns []transcript.Turn) error {
	data, err := json.MarshalIndent(turns, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}
