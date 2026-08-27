// Package models is the menu of speech models and where they live on disk.
// Nothing ships with the build: every model is downloaded into the user's
// cache, so they are all on the same footing and none is a special case.
package models

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/christian-oudard/diktat/internal/human"
)

// Spec is one entry in the menu.
type Spec struct {
	Name string
	// quant is the quantization published for this model. Whisper ships a
	// K-quant, moonshine only Q8_0.
	quant string
	// MiB is the download size in mebibytes, so the menu can show the cost
	// of fetching one. Measured from the published file, not converted from a
	// decimal figure: the two differ by 5% and the column says MiB.
	MiB int
	// Langs are the language codes the model advertises, or nil for a model
	// that takes most of them: whisper-large-v3-turbo lists a hundred, which
	// is not a menu column. Checked against the library the same way.
	Langs []string
}

// Languages renders the language support for the menu, as the reach of the
// set and its size.
//
// Someone choosing a model wants to know whether it will handle what they
// speak, and past three or four codes a list answers that worse than a name
// for the set does: "en +29" says nothing about whether Japanese is in there,
// and neither does the eight codes that would fit the column. Naming the reach
// says which question to stop asking, and the count says how thoroughly.
func (s Spec) Languages() string {
	switch {
	case len(s.Langs) == 0:
		// A model that lists a hundred, which no column can hold and no
		// caveat improves on.
		return "Worldwide"
	case len(s.Langs) <= 2:
		// Short enough to name outright, which beats naming the reach: a
		// model taking English and Chinese is not "Worldwide (2)" to anyone
		// who speaks a third language.
		names := make([]string, len(s.Langs))
		for i, code := range s.Langs {
			names[i] = language(code)
		}
		return strings.Join(names, ", ")
	case european(s.Langs):
		return fmt.Sprintf("European (%d)", len(s.Langs))
	}
	return fmt.Sprintf("Worldwide (%d)", len(s.Langs))
}

// europeanCodes are the languages of Europe as this menu counts them. The
// borderline cases do not decide anything here: every model that advertises
// Turkish also advertises Japanese and Chinese, so it reads as worldwide
// whichever way Turkish is counted.
var europeanCodes = map[string]bool{
	"bg": true, "cs": true, "da": true, "de": true, "el": true, "en": true,
	"es": true, "et": true, "fi": true, "fr": true, "hr": true, "hu": true,
	"it": true, "lt": true, "lv": true, "mk": true, "mt": true, "nl": true,
	"pl": true, "pt": true, "ro": true, "ru": true, "sk": true, "sl": true,
	"sv": true, "uk": true,
}

func european(langs []string) bool {
	for _, code := range langs {
		if !europeanCodes[code] {
			return false
		}
	}
	return true
}

// language names a lone language, since a model that takes exactly one should
// say which rather than make its code do the work. Only the codes that appear
// alone in the menu are named; anything else falls back to the code.
func language(code string) string {
	if name, ok := map[string]string{"en": "English", "zh": "Chinese"}[code]; ok {
		return name
	}
	return code
}

// Default is what the daemon loads when not told otherwise: the model that is
// usable on the machine that has no card, since that is what a default has to
// be. Measured on a minute of dictation it makes half the errors of the
// whisper-tiny.en it replaced, 18.4% against 36.8%, and takes 223ms on a CPU
// where that whisper took 983ms and the 0.6b parakeet takes 1.1s. Anyone with
// a GPU should move up to parakeet-tdt-0.6b-v2, which the README says.
const Default = "parakeet-tdt_ctc-110m"

// Catalog is the whole menu: as many of the models the library supports as
// can be carried easily, since an entry costs a line and nothing is bundled.
// WER is the Open ASR Leaderboard average over its eight short-form English
// sets, which is a better guide for dictation than LibriSpeech alone.
//
//	whisper-base.en                       English, flat cost at any length
//	parakeet-tdt_ctc-110m      6.6% WER   English, the default
//	canary-180m-flash                     4 languages at a tenth of the 1b
//	parakeet-tdt-0.6b-v2       5.4% WER   English
//	parakeet-tdt-0.6b-v3                  the v2 with 25 European languages
//	whisper-large-v3-turbo     7.0% WER   99 languages
//	Qwen3-ASR-0.6B                        30 languages, the widest small one
//	canary-1b-flash            5.8% WER   en/de/es/fr, and translation
//	Qwen3-ASR-1.7B                        the 0.6B's next size up
//	cohere-transcribe-03-2026             14 languages
//	granite-speech-4.1-2b-nar  4.9% WER   English, no timestamps
//	canary-qwen-2.5b                      English, an LLM for a decoder
//
// The blanks are models the leaderboard does not carry: whisper-base.en
// because it only measures large-v3-turbo, and the recent ones because it has
// not caught up with them. A number invented here would be worse than the
// blank. Those are in the menu to be tried, not because they are known good.
//
// canary-qwen-2.5b is the one architecture here that decodes with a language
// model rather than an ASR head, which is the only mechanism that could get a
// technical term right from context instead of from acoustics. It takes no
// instruction, so unlike Voxtral it cannot be talked into answering with
// something other than a transcript.
//
// Two shapes of model are here, and the difference matters more than the
// sizes do. Whisper always encodes a padded 30 second window, so it costs the
// same whatever was said; the rest encode only the audio they were given.
// Measured on this laptop's CPU, the smallest whisper against
// parakeet-tdt_ctc-110m:
//
//	 2s utterance   1045ms    136ms
//	 3s utterance    960ms    235ms
//	30s utterance   2365ms   2335ms
//	55s utterance   2639ms   4768ms
//
// So the flat cost is a liability up to about 30 seconds and an asset past
// it. Dictation is mostly short utterances, which is why the menu leads with
// the models that scale with the audio.
//
// The same window makes whisper the only family whose memory is flat too: its
// compute buffers cost the same 0.10 GiB on one second of audio as on five
// minutes, where every other family here grows with the length until the card
// cannot hold the graph (see Audio length in CLAUDE.md). That is why both
// whispers stay. whisper-large-v3-turbo has languages the others lack, and
// whisper-base.en is the cheap way to transcribe something long on a card
// with nothing to spare, which nothing else in this menu can do at all.
//
// Two .en whispers were dropped once there was something to compare them
// against. whisper-small.en at 184 MiB was beaten by parakeet-tdt_ctc-110m at
// 96, and whisper-tiny.en at 42 MiB by moonshine-tiny at 33: beaten outright
// on size and accuracy at once, which is the bar for leaving one out.
//
// moonshine-tiny is the floor: worth it only where nothing else fits.
// granite is the ceiling on accuracy, and cohere-transcribe on size; both are
// big enough that the cache budget will evict something to hold them.
//
// parakeet-tdt-0.6b-v3 is here beside v2 rather than instead of it. Nine more
// mebibytes buys twenty-four more languages, and it does cost English accuracy
// to get them: 4.07 against 3.52 on the leaderboard's close-microphone sets,
// and 15.4% against 12.5% on the clip measured here. Two measurements agreeing
// is why the README names v2 and not v3.
var Catalog = []Spec{
	{"moonshine-tiny", "Q8_0", 33, []string{"en"}},
	{"whisper-base.en", "Q5_K_M", 60, []string{"en"}},
	{"parakeet-tdt_ctc-110m", "Q5_K_M", 96, []string{"en"}},
	{"canary-180m-flash", "Q5_K_M", 151, []string{"en", "de", "es", "fr"}},
	{"parakeet-tdt-0.6b-v2", "Q5_K_M", 514, []string{"en"}},
	{"parakeet-tdt-0.6b-v3", "Q5_K_M", 523, []string{
		"en", "bg", "cs", "da", "de", "el", "es", "et", "fi", "fr", "hr", "hu",
		"it", "lt", "lv", "mt", "nl", "pl", "pt", "ro", "ru", "sk", "sl", "sv", "uk"}},
	{"whisper-large-v3-turbo", "Q5_K_M", 590, nil},
	{"Qwen3-ASR-0.6B", "Q5_K_M", 615, qwen3Langs},
	{"canary-1b-flash", "Q5_K_M", 733, []string{"en", "de", "es", "fr"}},
	{"Qwen3-ASR-1.7B", "Q5_K_M", 1447, qwen3Langs},
	{"cohere-transcribe-03-2026", "Q5_K_M", 1688, []string{
		"en", "ar", "de", "el", "es", "fr", "it", "ja", "ko", "nl", "pl", "pt", "vi", "zh"}},
	{"granite-speech-4.1-2b-nar", "Q5_K_M", 1699, []string{"en", "de", "es", "fr", "pt"}},
	{"canary-qwen-2.5b", "Q5_K_M", 1891, []string{"en"}},
}

// qwen3Langs is the set both Qwen3-ASR sizes advertise, which is the same set.
// Nothing writes to a Spec's Langs, so the two entries can share it.
var qwen3Langs = []string{
	"en", "ar", "cs", "da", "de", "el", "es", "fa", "fi", "fil", "fr", "hi",
	"hu", "id", "it", "ja", "ko", "mk", "ms", "nl", "pl", "pt", "ro", "ru",
	"sv", "th", "tr", "vi", "yue", "zh"}

// Dir is where downloaded models live.
func Dir() string {
	if cache := os.Getenv("XDG_CACHE_HOME"); cache != "" {
		return filepath.Join(cache, "diktat", "models")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "diktat", "models")
}

// File is the GGUF's name, which carries the quantization so two quants of
// one model can sit side by side in the cache.
func (s Spec) File() string {
	if e, ok := elsewhere[s.Name]; ok {
		return e.file
	}
	return fmt.Sprintf("%s-%s.gguf", s.Name, s.quant)
}

// elsewhere names the models whose GGUF is published outside the
// handy-computer org, which is one repo per model named after it. These two
// were converted and published by other people before this menu wanted them,
// and fetching what exists beats asking anybody to publish a second copy of
// the same weights. The library reads both spellings of them.
var elsewhere = map[string]struct{ repo, file string }{
	"fsmn-vad":      {"FunAudioLLM/fsmn-vad-GGUF", "fsmn-vad.gguf"},
	"titanet-large": {"cstr/titanet-large-GGUF", "titanet-large.gguf"},
}

// Size is the download, for the menu and for the prompt before fetching one.
func (s Spec) Size() string { return human.Bytes(uint64(s.MiB) << 20) }

// Path is where a menu entry lands once downloaded.
func (s Spec) Path() string { return filepath.Join(Dir(), s.File()) }

// Downloaded reports whether the model is present and complete.
func (s Spec) Downloaded() bool {
	return Check(s.Path()) == nil
}

// Lookup finds a menu entry by name, or by its position in the menu counting
// from 1. The names run to twenty-odd characters and the menu is short, so
// the number is what anyone switching models by hand will reach for. Names
// are matched first, so a model named for a number would still win.
func Lookup(nameOrNumber string) (Spec, bool) {
	for _, s := range Catalog {
		if s.Name == nameOrNumber {
			return s, true
		}
	}
	if n, err := strconv.Atoi(nameOrNumber); err == nil && n >= 1 && n <= len(Catalog) {
		return Catalog[n-1], true
	}
	return Spec{}, false
}

// Names lists the menu.
func Names() []string {
	out := make([]string, 0, len(Catalog))
	for _, s := range Catalog {
		out = append(out, s.Name)
	}
	return out
}

// Resolve turns a menu name into a path. Anything containing a separator is
// taken as a path and used as given, so an out-of-menu model still works.
func Resolve(nameOrPath string) string {
	if strings.ContainsRune(nameOrPath, filepath.Separator) || nameOrPath == "." {
		if abs, err := filepath.Abs(nameOrPath); err == nil {
			return abs
		}
		return nameOrPath
	}
	if s, ok := Lookup(nameOrPath); ok {
		return s.Path()
	}
	return filepath.Join(Dir(), nameOrPath)
}

// Check reports whether path holds a model the daemon can load. Whether the
// GGUF is one of the architectures the library implements is its business,
// not ours; this only rules out the obvious mistakes.
func Check(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s: a directory, not a .gguf", path)
	}
	if !strings.HasSuffix(path, ".gguf") {
		return fmt.Errorf("%s: not a .gguf", path)
	}
	// Every GGUF starts with these four bytes. Checking them turns two
	// confusing failures into one clear one: a download that was interrupted
	// between the .part rename and the disk finishing with it looks like a
	// model the menu says is present and the loader refuses, and a file that
	// was renamed to .gguf by hand looks the same. Both come back from the
	// library as "gguf load error", four words with no path in them.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != "GGUF" {
		return fmt.Errorf("%s: not a GGUF file", path)
	}
	return nil
}
