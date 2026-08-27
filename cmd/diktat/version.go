package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/christian-oudard/diktat/internal/ipc"
)

// Stamped in by the nix build via ldflags. The store path says whether two
// builds differ, not which commit either one is.
var (
	gitRev  = "unknown"
	gitDate = ""
)

// build names this binary's revision and when it was built. Shared with
// doctor, whose report means something different against a different build
// and which is the fact most likely to be left out of one.
func build() string {
	// Stamped as RFC3339 in UTC, because ldflags cannot carry a space and the
	// build clock is UTC. Show it in the reader's own timezone.
	if t, err := time.Parse(time.RFC3339, gitDate); err == nil {
		return fmt.Sprintf("%s (%s)", gitRev, t.Local().Format("2006-01-02 15:04"))
	}
	return gitRev
}

func runVersion(args []string) {
	log.SetFlags(0)
	fmt.Printf("diktat %s\n", build())

	// Only worth a second line when the running daemon is some other build, in
	// which case the revision above does not describe what is transcribing.
	pid := ipc.ReadPID()
	if pid == 0 {
		return
	}
	if running := ipc.ExePath(pid); running != exePath() {
		log.Printf("daemon is a different build (pid %d): %s", pid, running)
		log.Printf("restart it with: systemctl --user restart diktat")
	}
}

// exePath is the store path of this build, which is what distinguishes one
// build of diktat from another.
func exePath() string {
	path, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	return path
}
