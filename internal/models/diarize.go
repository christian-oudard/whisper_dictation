package models

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/christian-oudard/diktat/internal/human"
)

// Pipeline is one way to get a transcript that says who is speaking. It is a
// menu entry of its own rather than a flag on the model menu, because the
// question it answers is different: dictation asks which model transcribes
// best, and this asks what will come back with speaker labels on it at all.
//
// An entry is a whole answer, not a part of one. Diarizing takes two jobs
// done, transcription and attribution, and pairing them is not a choice worth
// leaving to whoever is choosing: sortformer produces no text and has to be
// paired with something, and the models that do both do them in one pass and
// cannot be paired with anything. So an entry names the models it needs, and
// a pairing worth having is worth its own entry.
type Pipeline struct {
	Name string
	// Models are what the entry needs downloaded, in the order they run. One
	// entry for a model that transcribes and attributes in the same pass; two
	// where the first supplies the words and the second the speaker rows they
	// are attributed to.
	Models []Spec
	// Speakers is the most it can tell apart, or 0 where the model declares
	// no limit. For a pair it is the diarizer's, since that is where the
	// identity comes from however many voices the transcriber can hear.
	//
	// This is the column that decides most choices here: everything descended
	// from sortformer stops at four however good it is otherwise, and a
	// recording with five voices in it does not fit them.
	Speakers int
	// Langs is hand-kept for the same reason the model menu's is: the listing
	// has to answer before anything is downloaded. It is the transcribing
	// model's set, since a diarizer attributes sound rather than words and
	// its own advertised set says nothing about what the pair can handle.
	Langs []string
	// Piece is the longest audio this entry should be given at once, or 0 for
	// a model that takes a whole recording. It is a property of the model
	// rather than a preference, and it is not only about memory: MOSS holds a
	// 15 minute clip comfortably and drifts into another language partway
	// through it, transcribing the rest of a workshop into Spanish. The same
	// audio in 2 minute pieces comes back correct.
	Piece time.Duration
	// Speech is a voice activity detector run over the whole recording
	// first, whose regions bound the windows the diarizer clusters. Zero for
	// an entry without one.
	//
	// It is a separate field rather than a third model because it answers a
	// different question: not who is speaking and not what they said, but
	// whether anybody is. A clustering diarizer needs it -- loudness cannot
	// tell a voice from a chair, and the chair then clusters as a thing that
	// is none of the voices and arrives as an extra speaker.
	Speech Spec
	// Note is what the listing cannot show: the tradeoff someone choosing
	// between two entries of the same size and cap is actually choosing
	// between.
	Note string
}

// Diarizers is the menu, ordered by what it costs to run, which here means
// memory rather than download: every entry fits on any card that fits its
// weights except the last, whose working set grows with the length of the
// recording.
//
// Three architectures reach a speaker-labelled transcript, and they are not
// variations on each other:
//
//   - Pair a diarizer with a model that supplies the words, and join the two
//     in transcript.Attribute. The join is ours to get right, and it is where
//     a naive implementation goes wrong: speaker rows and transcript segments
//     have different boundaries, so attribution has to be per word rather than
//     per segment.
//   - Run a model that was trained to do both. multitalker-parakeet embeds
//     the same sortformer in its own GGUF and hands back a transcript with
//     the speakers already on it.
//   - Run an audio-LLM that emits speaker tags as text. MOSS writes
//     [start][Sxx]text[end] and the runtime parses it.
//
// The published numbers are not comparable across those three, because they
// are scored on different things: parakeet's word error is measured on read
// audiobook speech, and multitalker's 19.35% is cpWER on sixteen real
// meetings, which counts an attribution mistake as an error. Only the second
// kind of number says anything about a room with several people in it.
//
// Nor do they rank within a family. parakeet-tdt-1.1b was in this menu on the
// strength of its 1.38% against the 0.6b-v2's 1.69%, and lost to it outright
// on two minutes of a real workshop recording: it dropped a name from a list
// of three and returned no punctuation or casing at all. LibriSpeech cannot
// see either failure, since its references are unpunctuated and its audio is
// one reader at a close microphone.
// DefaultDiarizer is what a recording is transcribed with before anybody has
// chosen. Not the first entry: the menu is ordered by what an entry costs and
// the default is about what it is likely to get right. Most recordings anybody
// points this at have one or two people in them, where a model trained to
// transcribe and attribute in one pass is steadier than clustering -- on three
// voices the clustering entry splits one of them in two, which a fixed-cap
// model cannot do. The entry to reach for past four speakers is in the menu
// and says so.
const DefaultDiarizer = "multitalker-parakeet"

var Diarizers = []Pipeline{
	{
		// The only entry that counts the speakers rather than being built
		// around a fixed number of them, which is what a recording of more
		// than four people needs. The detector is not optional here: without
		// it the clustering is handed whatever passed a loudness threshold,
		// and audible non-speech clusters as a thing that is none of the
		// voices -- an extra speaker on three of four AMI meetings.
		Name: "parakeet-tdt-0.6b-v2 + titanet",
		Models: []Spec{
			{"parakeet-tdt-0.6b-v2", "Q8_0", 696, []string{"en"}},
			{"titanet-large", "F16", 43, []string{"en"}},
		},
		Speech:   Spec{"fsmn-vad", "F32", 2, nil},
		Speakers: 20,
		Langs:    []string{"en"},
		Piece:    3 * time.Minute,
		Note:     "counts the speakers instead of stopping at four; splits a voice sooner than the fixed-cap entries do",
	},
	{
		Name: "multitalker-parakeet",
		Models: []Spec{
			{"multitalker-parakeet-streaming-0.6b-v1", "Q8_0", 833, []string{"en"}},
		},
		Speakers: 4,
		Langs:    []string{"en"},
		Note:     "one file, one pass; 19.35% cpWER on AMI meetings",
	},
	{
		Name: "parakeet-tdt-0.6b-v2 + sortformer",
		Models: []Spec{
			{"parakeet-tdt-0.6b-v2", "Q8_0", 696, []string{"en"}},
			{"diar_streaming_sortformer_4spk-v2.1", "F16", 226, []string{"en"}},
		},
		Speakers: 4,
		Langs:    []string{"en"},
		Piece:    3 * time.Minute,
		Note:     "the best words, attributed per word to a whole-file speaker track",
	},
	{
		Name: "parakeet-tdt-0.6b-v3 + sortformer",
		Models: []Spec{
			{"parakeet-tdt-0.6b-v3", "Q8_0", 706, parakeetV3Langs},
			{"diar_streaming_sortformer_4spk-v2.1", "F16", 226, []string{"en"}},
		},
		Speakers: 4,
		Langs:    parakeetV3Langs,
		Piece:    3 * time.Minute,
		Note:     "the same, for the 24 languages the v2 does not take",
	},
	{
		Name: "MOSS-Transcribe-Diarize",
		Models: []Spec{
			{"MOSS-Transcribe-Diarize", "Q8_0", 941, []string{"en", "zh"}},
		},
		Speakers: 0,
		Langs:    []string{"en", "zh"},
		Piece:    2 * time.Minute,
		Note:     "no speaker cap: found 10 where the others found 4, but only within a piece",
	},
	{
		Name: "MOSS-Transcribe-Diarize + sortformer",
		Models: []Spec{
			{"MOSS-Transcribe-Diarize", "Q8_0", 941, []string{"en", "zh"}},
			{"diar_streaming_sortformer_4spk-v2.1", "F16", 226, []string{"en"}},
		},
		Speakers: 4,
		Langs:    []string{"en", "zh"},
		Piece:    2 * time.Minute,
		Note:     "MOSS words on a speaker track that holds; slowest here by far",
	},
}

// parakeetV3Langs is the 25 European languages the v3 takes, which the model
// menu carries too. Nothing writes to a Spec's Langs, so both can share it.
var parakeetV3Langs = []string{
	"en", "bg", "cs", "da", "de", "el", "es", "et", "fi", "fr", "hr", "hu",
	"it", "lt", "lv", "mt", "nl", "pl", "pt", "ro", "ru", "sk", "sl", "sv", "uk"}

// Cap renders the speaker limit for the listing. Nothing declared is not the
// same as unlimited, and saying "unlimited" where the model simply never said
// would be inventing a guarantee.
func (p Pipeline) Cap() string {
	if p.Speakers == 0 {
		return "unstated"
	}
	return fmt.Sprintf("up to %d", p.Speakers)
}

// Size is what the whole entry costs to fetch, which for a pair is both
// halves: someone deciding whether to download one is deciding about the
// pipeline, not about a file in it.
func (p Pipeline) Size() string {
	mib := p.Speech.MiB
	for _, s := range p.Models {
		mib += s.MiB
	}
	return human.Bytes(uint64(mib) << 20)
}

// Languages renders the reach of the entry, the same way the model menu does.
func (p Pipeline) Languages() string { return Spec{Langs: p.Langs}.Languages() }

// Codes lists the language codes the entry takes, which is what the `-lang`
// setting is given. The menu column names the reach of a set because a column
// cannot hold 25 codes; that answers "roughly where" and not "is mine in
// there", and the second question is the one somebody about to spend an hour
// transcribing is asking.
func (p Pipeline) Codes() string {
	if len(p.Langs) == 0 {
		return "any language the model detects"
	}
	out := make([]string, len(p.Langs))
	for i, code := range p.Langs {
		out[i] = code
		if name := language(code); name != code {
			out[i] = fmt.Sprintf("%s (%s)", code, name)
		}
	}
	return strings.Join(out, " ")
}

// Takes reports whether the entry accepts this language hint. An entry that
// advertises no set takes anything: unlisted is not refused, since a model that
// names no languages is one that detects them rather than one that has none.
func (p Pipeline) Takes(code string) bool {
	if code == "" || len(p.Langs) == 0 {
		return true
	}
	return slices.Contains(p.Langs, code)
}

// Downloaded reports whether every model the entry needs is present. A pair
// with one half missing cannot run, so it is not downloaded.
func (p Pipeline) Downloaded() bool {
	for _, s := range p.All() {
		if !s.Downloaded() {
			return false
		}
	}
	return true
}

// All is every model the entry runs, the detector included. Downloading is
// both halves or neither, and so is this.
func (p Pipeline) All() []Spec {
	all := p.Models
	if p.Speech.Name != "" {
		all = append(append([]Spec{}, all...), p.Speech)
	}
	return all
}

// LookupDiarizer finds an entry by name or by its position in the menu,
// counting from 1, matching names first the way the model menu does.
func LookupDiarizer(nameOrNumber string) (Pipeline, bool) {
	for _, p := range Diarizers {
		if p.Name == nameOrNumber {
			return p, true
		}
	}
	if n, err := strconv.Atoi(nameOrNumber); err == nil && n >= 1 && n <= len(Diarizers) {
		return Diarizers[n-1], true
	}
	return Pipeline{}, false
}

// DownloadPipeline fetches whatever the entry is missing. Both halves or
// neither: a pair with one model on disk is not a thing anyone asked for.
func DownloadPipeline(p Pipeline, progress io.Writer) error {
	for _, s := range p.All() {
		if _, err := download(s, progress); err != nil {
			return err
		}
	}
	return nil
}
