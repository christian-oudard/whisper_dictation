// Repeat: re-type the last transcription.
package main

import (
	"log"
	"os"

	"github.com/christian-oudard/diktat/internal/config"
	"github.com/christian-oudard/diktat/internal/ipc"
	"github.com/christian-oudard/diktat/internal/output"
)

func runRepeat(args []string) {
	path, err := ipc.LastText()
	if err != nil {
		log.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Fatalf("read last text: %v", err)
	}
	// Unknown keys are the daemon's business to report; repeat only needs
	// the paste methods.
	cfg, _, err := config.Load(config.DefaultPath())
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := output.Type(string(raw), cfg.TypingMethods); err != nil {
		log.Fatalf("type: %v", err)
	}
}
