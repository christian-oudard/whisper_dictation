// diktat is voice dictation for Sway. One binary, one subcommand per job.
package main

import (
	"fmt"
	"os"
)

type command struct {
	name    string
	usage   string
	summary string
	run     func(args []string)
	// hidden keeps a command out of the usage text, and so out of the zsh
	// completion, which reads the command list back out of --help. For one
	// nobody has to find while dictating, only when reporting something the
	// README will have pointed them at.
	hidden bool
}

var commands = []command{
	{"daemon", "", "Run the Diktat voice transcription daemon.", runDaemon, false},
	{"toggle", "", "Start or stop recording.", runToggle, false},
	{"repeat", "", "Repeat the last transcription, typing the text again.", runRepeat, false},
	{"model", "[<model>]", "List, switch, or fetch voice transcription models.", runModel, false},
	{"transcribe", "<recording>", "Transcribe a recording into a document with speaker labels.", runTranscribe, false},
	{"tx-model", "[<pipeline>]", "List, choose, or fetch speaker-labelled transcription models.", runTxModel, false},
	{"version", "", "Report the build, and whether the daemon matches it.", runVersion, false},
	{"doctor", "", "Report this machine, for a bug report.", runDoctor, true},
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stdout)
		return
	}
	name := os.Args[1]
	if name == "-h" || name == "--help" || name == "help" {
		usage(os.Stdout)
		return
	}
	for _, c := range commands {
		if c.name == name {
			c.run(os.Args[2:])
			return
		}
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n", name)
	usage(os.Stderr)
	os.Exit(1)
}

// prompt asks a question on stderr, where every message this tool writes
// goes: the log does too, with its flags off, so the two are one stream and
// there is only ever one place to look. It exists next to log.Printf only
// because a question ends in a space rather than a newline.
//
// Stdout carries the answer to the command and nothing else -- the menu, the
// version -- since a message there is indistinguishable from data to whatever
// is parsing it.
func prompt(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: diktat <command> [args]")
	fmt.Fprintln(w)
	for _, c := range commands {
		if c.hidden {
			continue
		}
		// Wide enough for "transcribe" and "[<pipeline>]", which are the
		// longest of each column.
		fmt.Fprintf(w, "  %-10s %-16s %s\n", c.name, c.usage, c.summary)
	}
}
