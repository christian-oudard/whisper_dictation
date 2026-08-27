package models

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A server that sends a little and then stops without closing is the failure
// this exists for: no error, no progress, and a socket that will wait for as
// long as anyone lets it.
func TestStalledDownloadGivesUp(t *testing.T) {
	hang := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write([]byte("the first few bytes"))
		w.(http.Flusher).Flush()
		<-hang
	}))
	// Order matters: Close waits for the handler, and the handler waits for
	// this channel, so the channel has to be closed first. Defers run last in,
	// first out.
	defer server.Close()
	defer close(hang)

	resp, err := fetch(server.URL, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	start := time.Now()
	_, err = io.Copy(io.Discard, resp.Body)
	if err == nil {
		t.Fatal("a stalled download returned no error")
	}
	if !strings.Contains(err.Error(), "stalled") {
		t.Errorf("error is %q, want it to say the connection stalled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("gave up after %s, want about the patience it was given", elapsed)
	}
}

// A download that keeps arriving, however slowly, is not stalled. This is the
// half that matters for a large model on a thin link: the guard must not be a
// timeout on the whole transfer.
func TestSlowDownloadIsNotStalled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 10; i++ {
			_, _ = w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(30 * time.Millisecond)
		}
	}))
	defer server.Close()

	resp, err := fetch(server.URL, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("a slow but live download failed: %v", err)
	}
	if len(body) != 50 {
		t.Errorf("read %d bytes, want 50", len(body))
	}
}
