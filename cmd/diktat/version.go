package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/christian-oudard/diktat/internal/ipc"
)

// Stamped in at build time via ldflags. The store path says whether two builds
// differ, not which commit either one is.
//
// version is what a distribution package calls this; gitRev is what the build
// was made from. They answer different questions -- "which release is
// installed" and "exactly which source" -- and a package built from a tag has
// both, where a build from a working tree has only the second.
var (
	version = "dev"
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
	// The brackets are for a build whose source is not the release it names.
	// Every packaging recipe stamps the tag it built, so repeating it there
	// says nothing, and a tarball build has no revision to say.
	if detail := buildDetail(version, gitRev, gitDate); detail != "" {
		fmt.Printf("diktat %s (%s)\n", version, detail)
	} else {
		fmt.Printf("diktat %s\n", version)
	}

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

// buildDetail is what the version line says in brackets, and it is empty when
// there is nothing to say. Every distribution recipe stamps the tag it built,
// so a packaged build would otherwise read `diktat 1.0.0 (v1.0.0)`, and a
// build made by hand from a tarball has no revision at all and read
// `diktat 1.0.0 (unknown)`. Both are the version twice or a confession, and
// neither is what the brackets are for: they exist for a build whose source is
// not the release it calls itself.
func buildDetail(version, rev, date string) string {
	if rev == "unknown" || rev == version || rev == "v"+version {
		rev = ""
	}
	// Stamped as RFC3339 in UTC, because ldflags cannot carry a space and the
	// build clock is UTC. Show it in the reader's own timezone.
	when := ""
	if t, err := time.Parse(time.RFC3339, date); err == nil {
		when = t.Local().Format("2006-01-02 15:04")
	}
	switch {
	case rev != "" && when != "":
		return rev + ", " + when
	case rev != "":
		return rev
	}
	return when
}
