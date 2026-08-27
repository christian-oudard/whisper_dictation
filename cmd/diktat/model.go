package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"

	"syscall"

	"github.com/christian-oudard/diktat/internal/config"
	"github.com/christian-oudard/diktat/internal/ipc"
	"github.com/christian-oudard/diktat/internal/models"
)

// runModel: bare lists the menu and offers to switch, anything else switches
// to that model, fetching it first if it is not in the cache yet.
func runModel(args []string) {
	log.SetFlags(0)
	if len(args) > 0 {
		switchModel(args[0])
		return
	}

	inUse := listModels()

	// Only offer the choice when the listing is being read by a person.
	// Piped, this command is how the zsh completion learns the menu, and a
	// prompt there would hang the shell waiting for an answer nobody is
	// there to give.
	if !terminal(os.Stdout) {
		return
	}
	if choice := askWhich(inUse); choice != "" {
		switchModel(choice)
	}
}

// askWhich offers the menu numbers, with an empty answer meaning "leave it
// alone" so the listing stays usable as a listing.
func askWhich(inUse string) string {
	keep := "change nothing"
	if inUse != "" {
		keep = "keep " + inUse
	}
	prompt("\nSelect 1-%d, or Enter to %s: ", len(models.Catalog), keep)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		prompt("\n")
		return ""
	}
	return strings.TrimSpace(line)
}

// listModels numbers the menu, since the names are long and switching by
// hand is the common case. The name stays in the second column so completion
// can read it, and the languages go last because that field is the one with
// no fixed width. It returns the menu name of the model in use, or "".
func listModels() string {
	// What the marker points at: the running daemon's model where there is
	// one, and otherwise the model a daemon started now would load. Both
	// answer "which model do I get", which is what the menu is read for, and
	// with nothing running the second is the only answer there is.
	current := loadedModel()
	if current == "" {
		current = models.Resolve(config.StartModel())
	}
	// What it is doing about it, which the model in use cannot say: a switch
	// to a 1.8 GiB model is tens of seconds where the menu would otherwise
	// look like nothing had happened.
	doing, subject := activity()

	// The number is right-aligned because the menu is past ten entries, and a
	// ragged one shifts every column after it on the rows that need it most.
	fmt.Printf("  %2s %-28s %8s  %s  %s\n",
		"#", "Name", "Size", "Downloaded", "Languages")
	inUse, busy := "", subject
	for i, s := range models.Catalog {
		mark := " "
		switch s.Path() {
		case current:
			mark, inUse = "*", s.Name
		case subject:
			// A model on its way in, which is not the one in use and has its
			// own mark rather than borrowing that one.
			mark = ">"
		}
		if s.Path() == subject {
			busy = s.Name
		}
		fmt.Printf("%s %2d %-28s %8s  %s  %s\n", mark, i+1, s.Name, s.Size(),
			tick(s.Downloaded(), "Downloaded"), s.Languages())
	}
	switch doing {
	case "loading":
		log.Printf("Loading %s", busy)
	case "warming":
		// Said plainly, because the honest answer to "is it ready" here is
		// yes: a rehearsal costs the first dictation at an unseen length, not
		// the ability to have one.
		log.Printf("Warming %s, which is usable meanwhile", busy)
	}
	// A model outside the menu gets no marker, so there would otherwise be
	// nothing anywhere saying what is in use. Say when it is not there: a
	// remembered choice outlives the menu entry it was made from, so dropping
	// a model leaves whoever had chosen it pointed at a path that no longer
	// resolves, and the daemon's failure to load it is a poor place to find
	// that out.
	if inUse == "" {
		missing := ""
		if models.Check(current) != nil {
			missing = "  (not there; pick another)"
		}
		log.Printf("Using %s%s", current, missing)
	}
	return inUse
}

// tick centres a mark under its header, so a column of them reads as a column
// rather than trailing off the header's left edge.
func tick(yes bool, header string) string {
	if !yes {
		return strings.Repeat(" ", len(header))
	}
	left := (len(header) - 1) / 2
	return strings.Repeat(" ", left) + "*" + strings.Repeat(" ", len(header)-1-left)
}

// loadedModel is what the running daemon has, or "" if none is running.
func loadedModel() string { return daemonFile(ipc.ModelPath) }

// activity is what the daemon is doing about a model that is not the one in
// use yet: the word it published and the model it applies to, or "" and "".
func activity() (string, string) {
	word, dir, _ := strings.Cut(daemonFile(ipc.ActivityPath), " ")
	return word, dir
}

// daemonFile reads one of the daemon's runtime files, and reads nothing when
// there is no daemon: these outlive a process that was killed outright, and a
// menu that says a dead daemon is loading something is worse than one that
// says nothing.
func daemonFile(name func() (string, error)) string {
	if ipc.ReadPID() == 0 {
		return ""
	}
	path, err := name()
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// switchModel points the daemon at a model, given its menu number, its name,
// or a path. A menu entry that is not in the cache is offered for download
// rather than refused, since wanting to use a model is the only reason to
// name one.
func switchModel(nameOrNumber string) {
	path := models.Resolve(nameOrNumber)
	spec, inMenu := models.Lookup(nameOrNumber)

	// Sort the model out before the daemon: a typo is the likelier mistake,
	// and it is fixable without starting anything.
	if err := models.Check(path); err != nil {
		if !inMenu {
			log.Fatalf("unknown model %q", nameOrNumber)
		}
		if !confirm(fmt.Sprintf("%s is not downloaded. Fetch it now (%s)?", spec.Name, spec.Size())) {
			log.Fatal("cancelled")
		}
		p, err := models.Download(spec.Name, os.Stderr)
		if err != nil {
			log.Fatal(err)
		}
		path = p
	}

	// Remember the choice before acting on it, so it holds whether or not a
	// daemon is up to hear about it. What gets recorded is the menu name
	// where there is one, never the menu number, which would mean something
	// else if the menu were reordered.
	remembered := nameOrNumber
	if inMenu {
		remembered = spec.Name
	}
	if err := config.Select(remembered); err != nil {
		log.Printf("could not remember the choice: %v", err)
	}

	// A daemon that is up gets told; one that is not will read the choice when
	// it starts.
	pid := ipc.ReadPID()
	if pid == 0 {
		log.Printf("Using %s", remembered)
		return
	}
	// What the daemon has now, read before the request overwrites it, so this
	// can say which of the two things is about to happen. A 1.8 GiB model is
	// seconds away from being the model in use, and "using" was the only word
	// anyone got while the daemon was still reading it off disk.
	loaded := loadedModel()
	modelPath, err := ipc.ModelPath()
	if err != nil {
		log.Fatal(err)
	}
	if err := ipc.Write(modelPath, []byte(path), 0644); err != nil {
		log.Fatalf("write model file: %v", err)
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		log.Fatalf("signal daemon: %v", err)
	}
	if path == loaded {
		log.Printf("Using %s", remembered)
		return
	}
	log.Printf("Switching to %s", remembered)
}

// confirm asks before spending someone's bandwidth, since a model runs to a
// couple of gigabytes. Yes is the default, so nothing to read counts as yes:
// stdin closed or coming from /dev/null means nobody is there to answer, and
// naming a model is intent enough on its own.
func confirm(question string) bool {
	prompt("%s [Y/n] ", question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		prompt("y\n")
		return true
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	}
	return false
}

// terminal reports whether f is a character device, which is what makes a
// prompt worth printing. A pipe or a file is something reading the output,
// not someone answering it.
func terminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
