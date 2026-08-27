package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/christian-oudard/diktat/internal/config"
	"github.com/christian-oudard/diktat/internal/models"
)

// runTxModel: bare lists the diarizer menu and offers to choose, anything else
// chooses that entry, fetching what it needs first.
//
// Separate from `diktat model` because the two menus answer different
// questions and share no entries. Dictation is one known voice saying a
// sentence, and picks on latency and on what fits beside a desktop; a
// recording of several people is picked on whether the labels come back right,
// which is a property of the pipeline rather than of a model. Nothing in the
// dictation menu diarizes at all, and the models here are useless for
// dictation: the smallest is 800 MiB and the cheapest of them produces no text.
func runTxModel(args []string) {
	log.SetFlags(0)
	if len(args) > 0 {
		chooseTxModel(args[0])
		return
	}

	inUse := listTxModels()

	// Only prompt for a person. Piped, this is how the completion learns the
	// menu, and a prompt there hangs the shell.
	if !terminal(os.Stdout) {
		return
	}
	keep := "change nothing"
	if inUse != "" {
		keep = "keep " + inUse
	}
	prompt("\nSelect 1-%d, or Enter to %s: ", len(models.Diarizers), keep)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		prompt("\n")
		return
	}
	if choice := strings.TrimSpace(line); choice != "" {
		chooseTxModel(choice)
	}
}

// listTxModels numbers the menu. Speakers is here and not on the other menu
// because it is what rules entries out: everything descended from sortformer
// stops at four, so a recording with five voices in it has exactly one entry
// that fits, whatever the other columns say. It returns the entry in use.
func listTxModels() string {
	current := config.TxModel()

	fmt.Printf("  %2s %-36s %9s  %s  %-9s %s\n",
		"#", "Name", "Size", "Downloaded", "Speakers", "Languages")
	inUse := ""
	for i, p := range models.Diarizers {
		mark := " "
		if p.Name == current {
			mark, inUse = "*", p.Name
		}
		fmt.Printf("%s %2d %-36s %9s  %s  %-9s %s\n", mark, i+1, p.Name, p.Size(),
			tick(p.Downloaded(), "Downloaded"), p.Cap(), p.Languages())
	}
	// The tradeoffs, under the table rather than in it: they are the reason to
	// pick one entry over another of the same size and cap, and none of them
	// fits a column.
	fmt.Println()
	for i, p := range models.Diarizers {
		fmt.Printf("  %2d %s\n", i+1, p.Note)
	}
	if inUse == "" && current != "" {
		log.Printf("Using %s, which is not in this menu", current)
	}
	return inUse
}

// chooseTxModel records an entry as the one to transcribe recordings with,
// given its menu number or its name, downloading what it needs first. Unlike
// `diktat model` there is no daemon to tell: nothing holds these loaded, since
// a recording is transcribed by a command that runs once and exits.
func chooseTxModel(nameOrNumber string) {
	p, ok := models.LookupDiarizer(nameOrNumber)
	if !ok {
		log.Fatalf("unknown pipeline %q", nameOrNumber)
	}
	if !p.Downloaded() {
		if !confirm(fmt.Sprintf("%s is not downloaded. Fetch it now (%s)?", p.Name, p.Size())) {
			log.Fatal("cancelled")
		}
		if err := models.DownloadPipeline(p, os.Stderr); err != nil {
			log.Fatal(err)
		}
	}
	if err := config.SelectTx(p.Name); err != nil {
		log.Printf("could not remember the choice: %v", err)
	}
	log.Printf("Using %s", p.Name)
}
