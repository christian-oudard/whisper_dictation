package models

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/christian-oudard/diktat/internal/human"
)

// hfOrg publishes the GGUF conversion of every model in the menu, one repo
// per model named after it.
const hfOrg = "https://huggingface.co/handy-computer"

// Download fetches a menu entry into the cache and returns where it landed.
// Files already present are left alone, so re-running is cheap. Nothing here
// downloads on its own: a caller has to ask for a model by name.
func Download(name string, progress io.Writer) (string, error) {
	spec, ok := Lookup(name)
	if !ok {
		return "", fmt.Errorf("unknown model %q", name)
	}
	return download(spec, progress)
}

// download is the same for a model reached through either menu.
func download(spec Spec, progress io.Writer) (string, error) {
	if err := os.MkdirAll(Dir(), 0755); err != nil {
		return "", err
	}
	dest := spec.Path()
	url := fmt.Sprintf("%s/%s-gguf/resolve/main/%s%s", hfOrg, spec.Name, dir(spec), spec.File())
	return dest, get(url, dest, progress)
}

// dir is the directory the GGUF sits in within its repo. Almost always the
// root, and for the multitalker parakeet a choice: upstream publishes the same
// file twice, plain and under bundle/ with the sortformer diarizer embedded in
// it. Only the bundle is in a menu here, since embedded speaker attribution is
// the whole reason to reach for that model, so nothing has to name the
// difference and the cached file keeps the upstream name.
func dir(spec Spec) string {
	if spec.Name == "multitalker-parakeet-streaming-0.6b-v1" {
		return "bundle/"
	}
	return ""
}

func get(url, dest string, progress io.Writer) error {
	if info, err := os.Stat(dest); err == nil && info.Size() > 0 {
		fmt.Fprintf(progress, "have     %s\n", filepath.Base(dest))
		return nil
	}

	resp, err := fetch(url, patience)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}

	// Write under a temporary name so an interrupted download is not mistaken
	// for a complete one next time.
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bar := newProgress(progress, filepath.Base(dest), resp.ContentLength)
	got, err := io.Copy(f, io.TeeReader(resp.Body, bar))
	if err == nil && resp.ContentLength >= 0 && got != resp.ContentLength {
		// A body that ended early without saying so. It happens, and nothing
		// downstream notices: the file is a valid path with a valid name, and
		// the only symptom is the library refusing to load a GGUF whose last
		// tensor runs off the end of it, hours or weeks later.
		err = fmt.Errorf("%s: got %s of %s", filepath.Base(dest),
			human.Bytes(uint64(got)), human.Bytes(uint64(resp.ContentLength)))
	}
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	bar.done()
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// progress draws a download's state on one rewritten line. The models run to
// a couple of gigabytes over a home connection, so a silent wait of several
// minutes reads as a hang.
type progress struct {
	w     io.Writer
	name  string
	total int64 // -1 when the server sent no length

	got     int64
	started time.Time
	last    time.Time
	// tty says whether the line can be rewritten. A log file or a pipe gets
	// one line at the end instead of thousands of redraws.
	tty bool
}

func newProgress(w io.Writer, name string, total int64) *progress {
	now := time.Now()
	p := &progress{w: w, name: name, total: total, started: now, last: now, tty: isTerminal(w)}
	if !p.tty {
		fmt.Fprintf(w, "fetching %s\n", name)
	}
	return p
}

func (p *progress) Write(b []byte) (int, error) {
	p.got += int64(len(b))
	// 20 Hz is smooth to read and costs nothing next to the download.
	if p.tty && time.Since(p.last) >= 50*time.Millisecond {
		p.last = time.Now()
		p.draw()
	}
	return len(b), nil
}

func (p *progress) done() {
	if p.tty {
		p.draw()
		fmt.Fprintln(p.w)
		return
	}
	fmt.Fprintf(p.w, "fetched  %s (%s in %s)\n",
		p.name, human.Bytes(uint64(p.got)), time.Since(p.started).Round(time.Second))
}

func (p *progress) draw() {
	rate := ""
	if secs := time.Since(p.started).Seconds(); secs > 0.5 {
		rate = fmt.Sprintf("  %.1f MiB/s", float64(p.got)/secs/human.MiB)
	}
	// No length means no bar and no percentage; show what has arrived.
	if p.total <= 0 {
		fmt.Fprintf(p.w, "\r\033[K%s  %s%s", p.name, human.Bytes(uint64(p.got)), rate)
		return
	}
	const width = 24
	frac := float64(p.got) / float64(p.total)
	filled := int(frac * width)
	bar := strings.Repeat("=", filled) + strings.Repeat(" ", width-filled)
	fmt.Fprintf(p.w, "\r\033[K%s  [%s] %3.0f%%  %s / %s%s",
		p.name, bar, frac*100, human.Bytes(uint64(p.got)), human.Bytes(uint64(p.total)), rate)
}

// isTerminal reports whether w is a character device, which is what makes
// rewriting a line with \r sensible.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
