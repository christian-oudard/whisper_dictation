// transcribe runs the daemon's own pipeline over WAV files, so a model or a
// preprocessing change can be measured against fixed audio instead of against
// a fresh utterance every time. Pass -raw to skip normalization and hear what
// the model makes of the untouched capture.
//
// Deliberately not a diktat subcommand: nothing here is part of dictating, so
// it is not worth a place in the shipped binary. The flake builds only
// cmd/diktat, which leaves this to `go run ./cmd/transcribe` inside the
// devShell. It lives in Go rather than in a script because it has to run the
// real pipeline, and a script would have to reimplement it.
//
//	go run ./cmd/transcribe [-raw] [-model <name>] file.wav...
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	transcribe "github.com/handy-computer/transcribe.cpp/bindings/go"

	"github.com/christian-oudard/diktat/internal/asr"
	"github.com/christian-oudard/diktat/internal/audio"
	"github.com/christian-oudard/diktat/internal/human"
	"github.com/christian-oudard/diktat/internal/models"
	"github.com/christian-oudard/diktat/internal/warmup"
	"github.com/christian-oudard/diktat/internal/wav"
)

// transcribeWith runs the daemon's own call, or the same with punctuation
// asked for. The default is what the daemon does, since that is what this
// tool exists to measure; the flag is here because the family defaults differ
// and the difference is not cosmetic. parakeet-tdt-0.6b-v2 punctuates
// unasked and parakeet-tdt-1.1b returns none, which decides whether either is
// usable in a document, and neither advertises the feature.
func transcribeWith(m *asr.Model, samples []float32, pnc bool) (string, error) {
	if !pnc {
		return m.Transcribe(context.Background(), samples)
	}
	res, err := m.Run(context.Background(), samples, &transcribe.RunOptions{PNC: transcribe.ModeOn})
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

func main() {
	fs := flag.NewFlagSet("transcribe", flag.ExitOnError)
	raw := fs.Bool("raw", false, "skip normalization")
	pnc := fs.Bool("pnc", false, "ask for punctuation and casing rather than taking the family default")
	limitFlag := fs.Duration("limit", 0, "cut audio at this length instead of what the model can take")
	name := fs.String("model", models.Default, "model to transcribe with")
	fs.Parse(os.Args[1:])

	modelPath := models.Resolve(*name)
	if err := models.Check(modelPath); err != nil {
		log.Fatalf("%s is not downloaded. Get it with:\n  diktat model %s", *name, *name)
	}
	model, err := asr.Load(modelPath)
	if err != nil {
		log.Fatalf("load model: %v", err)
	}
	defer model.Close()
	// Warmed like the daemon warms, which is not only about the first file's
	// timings. A model that has run nothing does not know how much audio it
	// can take in one graph and falls back to a 30 second floor, and cutting
	// a clip there changes what comes back: on this passage it cost
	// canary-180m-flash 19 points of word error rate and saved parakeet 4.
	if _, err := warmup.Run(context.Background(), model); err != nil {
		log.Printf("warmup: %v", err)
	}
	// The limit is part of what a model is, not a detail: it says how much of
	// an utterance this one takes in a single graph before the audio has to be
	// cut, which on a big model on a small card is under a minute.
	fmt.Printf("%s, %s resident, good for %s of audio (%s)\n", model.Arch(),
		human.Bytes(model.Bytes()), model.AudioLimit().Round(time.Second),
		model.LoadTimings())

	for _, path := range fs.Args() {
		stored, err := load(path)
		if err != nil {
			log.Printf("%v", err)
			continue
		}
		peak, rms := audio.Levels(stored)
		gain := 1.0
		if !*raw {
			gain = audio.Gain(stored)
		}
		t0 := time.Now()
		// Cut and padded like the daemon does it, so a file measures what an
		// utterance of that length would cost, down to the graph shape.
		limit := model.AudioLimit()
		if *limitFlag != 0 {
			limit = *limitFlag
		}
		var parts []string
		fail := false
		for _, chunk := range audio.Chunk(stored, int(limit.Seconds())*audio.SampleRate) {
			part, err := transcribeWith(model, audio.Pad(audio.Floats(chunk, gain)), *pnc)
			if err != nil {
				log.Printf("%s: transcribe: %v", path, err)
				fail = true
				break
			}
			parts = append(parts, part)
		}
		if fail {
			continue
		}
		text := strings.Join(parts, " ")
		// The first file pays for one-off setup, such as compiling the GPU
		// shaders, so compare later ones when timing a backend.
		fmt.Printf("%-24s %5.1fs  peak %.3f  rms %.4f  gain %4.1fx  %6s  ->  %q\n",
			path, float64(len(stored))/float64(audio.SampleRate), peak, rms, gain,
			time.Since(t0).Round(time.Millisecond), text)
	}
}

// load reads a wav into the form a capture is held in, so a file follows
// exactly the path a recording does.
func load(path string) ([]int16, error) {
	samples, rate, err := wav.ReadWAV(path)
	if err != nil {
		return nil, err
	}
	if rate != audio.SampleRate {
		return nil, fmt.Errorf("%s: sample rate %d != %d", path, rate, audio.SampleRate)
	}
	return audio.Ints(samples), nil
}
