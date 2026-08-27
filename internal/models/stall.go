package models

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// patience is how long a download may deliver nothing before it is treated as
// dead.
//
// Not a timeout on the download: these files are hundreds of megabytes and a
// slow link is a slow link, not a failure. What is a failure is a connection
// that stops delivering and never says so, which is what happened here -- a
// fetch sat at 76% for two hours, holding an open socket, with no error and no
// progress, until it was killed by hand. A stalled TCP connection can wait
// like that indefinitely; nothing below the application layer will time it
// out.
//
// A minute is long against any real pause between packets and short against an
// evening.
const patience = time.Minute

// stalled wraps a response body so that a gap in delivery cancels the request
// rather than waiting forever. Every read resets the clock; the first one that
// does not arrive in time cancels the context the request was made with, which
// closes the connection and surfaces as an error from Read.
type stalled struct {
	body   io.ReadCloser
	timer  *time.Timer
	wait   time.Duration
	cancel context.CancelFunc

	mu   sync.Mutex
	dead bool
}

func guard(body io.ReadCloser, cancel context.CancelFunc, wait time.Duration) *stalled {
	s := &stalled{body: body, wait: wait, cancel: cancel}
	s.timer = time.AfterFunc(wait, func() {
		s.mu.Lock()
		s.dead = true
		s.mu.Unlock()
		cancel()
	})
	return s
}

func (s *stalled) Read(p []byte) (int, error) {
	n, err := s.body.Read(p)
	if n > 0 {
		s.timer.Reset(s.wait)
	}
	if err != nil {
		s.mu.Lock()
		dead := s.dead
		s.mu.Unlock()
		if dead {
			// The cancellation was ours, so say what it means. "context
			// canceled" describes the mechanism and tells the user nothing.
			return n, fmt.Errorf("no data for %s: the connection stalled", s.wait)
		}
	}
	return n, err
}

func (s *stalled) Close() error {
	s.timer.Stop()
	s.cancel()
	return s.body.Close()
}

// fetch performs the GET with a guard on every stage that can hang: the
// connection, the TLS handshake, waiting for headers, and the body itself.
// There is deliberately no timeout on the whole request, which is the usual
// mistake -- it would kill a large download on a slow link for being large and
// slow.
//
// The returned body must be closed, which also releases the guard.
func fetch(url string, wait time.Duration) (*http.Response, error) {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = guard(resp.Body, cancel, wait)
	return resp, nil
}
