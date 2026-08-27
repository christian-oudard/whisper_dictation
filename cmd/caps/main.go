// caps says what a model can do, read out of the GGUF rather than out of a
// table someone kept by hand. The menu's own listing has to answer before a
// model is downloaded, so it carries only what a person can write down; this
// carries what the library knows once the file is on disk.
//
// It exists because timestamps became a requirement. Attributing text to
// speakers means lining words up against speaker rows, so a model that stamps
// only whole segments cannot be joined to a diarizer however good its
// transcripts are, and that is not something the download size says.
//
//	go run ./cmd/caps [model...]
//
// With no arguments it reads every model already in the cache, on the CPU,
// since none of this needs the card.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	transcribe "github.com/handy-computer/transcribe.cpp/bindings/go"

	"github.com/christian-oudard/diktat/internal/asr"
	"github.com/christian-oudard/diktat/internal/human"
	"github.com/christian-oudard/diktat/internal/models"
)

func main() {
	// Capabilities come out of the file, so there is nothing here worth
	// putting on the card, and a menu's worth of loads would evict whatever
	// the daemon is holding.
	os.Setenv("DIKTAT_GPU", "0")

	devices()

	paths := os.Args[1:]
	if len(paths) == 0 {
		paths = cached()
	}
	for _, p := range paths {
		describe(models.Resolve(p))
	}
}

// devices says what the machine has and how much of it is spare. Free memory
// is the number that decides whether a recording can be transcribed beside a
// running daemon at all: both want the same card, the daemon's compute buffers
// grow with the length of a dictation, and neither asks the other first.
func devices() {
	list, err := transcribe.Devices()
	if err != nil {
		fmt.Printf("devices: %v\n\n", err)
		return
	}
	for _, d := range list {
		if d.MemoryTotal == 0 {
			fmt.Printf("%-42s  %s\n", d.Description, kind(d.Type))
			continue
		}
		fmt.Printf("%-42s  %s, %s free of %s\n", d.Description, kind(d.Type),
			human.Bytes(d.MemoryFree), human.Bytes(d.MemoryTotal))
	}
	fmt.Println()
}

func kind(t transcribe.DeviceType) string {
	switch t {
	case transcribe.DeviceGPU:
		return "GPU"
	case transcribe.DeviceIGPU:
		return "iGPU"
	case transcribe.DeviceAccel:
		return "accelerator"
	}
	return "CPU"
}

// cached is every GGUF in the model directory, which is the set worth
// reporting on: a model that is not downloaded cannot be asked.
func cached() []string {
	entries, err := os.ReadDir(models.Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gguf") {
			out = append(out, filepath.Join(models.Dir(), e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

func describe(path string) {
	name := strings.TrimSuffix(filepath.Base(path), ".gguf")
	model, err := asr.Load(path)
	if err != nil {
		fmt.Printf("%-42s  %v\n", name, err)
		return
	}
	defer model.Close()

	langs, _ := model.Languages()
	fmt.Printf("%-42s  %-8s  %-9s  %s\n", name,
		stamps(model), audio(model.MaxAudio()), features(model, len(langs)))
}

// stamps is the finest alignment the model can produce, which is the one
// property that decides whether it can carry speaker labels at all.
func stamps(m *asr.Model) string {
	for _, k := range []transcribe.Timestamps{transcribe.StampsToken, transcribe.StampsWord, transcribe.StampsSegment} {
		if m.MaxTimestamps() >= k {
			return k.String()
		}
	}
	return m.MaxTimestamps().String()
}

// audio is the model's own declared ceiling on one run. Zero means the family
// windows long audio internally, which is not the same as no limit: the card
// still has to hold the graph (see Audio length in CLAUDE.md).
func audio(d time.Duration) string {
	if d == 0 {
		return "chunks"
	}
	return d.Round(time.Second).String()
}

func features(m *asr.Model, langs int) string {
	var on []string
	for _, f := range []struct {
		name string
		flag transcribe.Feature
	}{
		{"diarize", transcribe.FeatureDiarization},
		{"long-form", transcribe.FeatureLongForm},
		{"pnc", transcribe.FeaturePNC},
		{"itn", transcribe.FeatureITN},
		{"prompt", transcribe.FeatureInitialPrompt},
	} {
		if m.Supports(f.flag) {
			on = append(on, f.name)
		}
	}
	if langs > 0 {
		on = append(on, fmt.Sprintf("%d langs", langs))
	}
	return strings.Join(on, ", ")
}
