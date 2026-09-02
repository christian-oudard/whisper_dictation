package wayland

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A compositor that answers requests, rather than a script that replays
// events regardless of what was asked.
//
// The difference is the whole point. Every other fake here is built from this
// package's own constants -- globalEvent sends registryGlobalEvent, replay
// keys on registryID -- so a constant that is wrong moves both sides together
// and the test still passes. That is how get_registry sat at 0, the same
// number as sync, under six wire tests: nothing ever asked a compositor for a
// registry and checked that it got one.
//
// So everything below is written in the numbers the protocol declares, quoted
// from the XML, and nothing below refers to a constant in this package. If the
// two disagree, this fails.
//
//	wl_display:  request sync 0, get_registry 1; event error 0, delete_id 1
//	wl_registry: request bind 0;                 event global 0, global_remove 1
//	wl_callback:                                 event done 0
//	zwp_input_method_manager_v2: request get_input_method 0, destroy 1
//	zwp_input_method_v2: request commit_string 0, commit 3
//	                     event activate 0, deactivate 1, done 5, unavailable 6
type compositor struct {
	// What the registry advertises, and whether a text input holds focus.
	offers  []string
	focused bool
	// unavailable answers get_input_method with unavailable instead of a
	// working object, which is a seat another input method already holds.
	unavailable bool

	mu       sync.Mutex
	out      bytes.Buffer
	registry uint32
	manager  uint32
	method   uint32
	dones    uint32

	// What arrived, for the test to check.
	text   string
	serial uint32
	bound  []string
}

// emit queues an event. The framing is the same as a request: the object, then
// the size and opcode packed into one word, then the arguments.
func (c *compositor) emit(object uint32, opcode uint16, body []byte) {
	head := make([]byte, 8)
	order.PutUint32(head[0:], object)
	order.PutUint32(head[4:], uint32(8+len(body))<<16|uint32(opcode))
	c.out.Write(append(head, body...))
}

// str encodes a wire string: a length counting the NUL, the bytes, the NUL,
// and padding to the next word. Written out rather than borrowed from args, so
// a mistake in the encoder cannot cancel out against itself.
func str(s string) []byte {
	b := make([]byte, 4)
	order.PutUint32(b, uint32(len(s)+1))
	b = append(b, s...)
	b = append(b, 0)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// takeStr reads one back, returning the rest.
func takeStr(b []byte) (string, []byte) {
	n := int(order.Uint32(b))
	padded := 4 + (n+3)&^3
	return string(b[4 : 4+n-1]), b[padded:]
}

// handle answers one request.
func (c *compositor) handle(object uint32, opcode uint16, body []byte) {
	switch {
	case object == 1 && opcode == 0: // wl_display.sync(callback)
		cb := order.Uint32(body)
		serial := make([]byte, 4)
		order.PutUint32(serial, 716608)
		c.emit(cb, 0, serial) // wl_callback.done
		id := make([]byte, 4)
		order.PutUint32(id, cb)
		c.emit(1, 1, id) // wl_display.delete_id, as sway sends after a callback

	case object == 1 && opcode == 1: // wl_display.get_registry(registry)
		c.registry = order.Uint32(body)
		for i, iface := range c.offers {
			body := make([]byte, 4)
			order.PutUint32(body, uint32(i+1)) // the compositor's own handle
			body = append(body, str(iface)...)
			version := make([]byte, 4)
			order.PutUint32(version, 1)
			c.emit(c.registry, 0, append(body, version...)) // wl_registry.global
		}

	case c.registry != 0 && object == c.registry && opcode == 0: // wl_registry.bind
		iface, rest := takeStr(body[4:])
		id := order.Uint32(rest[4:]) // past the version
		c.bound = append(c.bound, iface)
		if iface == "zwp_input_method_manager_v2" {
			c.manager = id
		}

	case c.manager != 0 && object == c.manager && opcode == 0: // get_input_method(seat, id)
		c.method = order.Uint32(body[4:])
		if c.unavailable {
			c.emit(c.method, 6, nil) // zwp_input_method_v2.unavailable
			return
		}
		if c.focused {
			c.emit(c.method, 0, nil) // activate
		}
		c.emit(c.method, 5, nil) // done, which applies whatever is pending
		c.dones++

	case c.method != 0 && object == c.method && opcode == 0: // commit_string(text)
		c.text, _ = takeStr(body)

	case c.method != 0 && object == c.method && opcode == 3: // commit(serial)
		c.serial = order.Uint32(body)
	}
}

// serve runs the fake on a unix socket and returns its path, so a test drives
// the package's real entry points rather than reaching inside them.
func serve(t *testing.T, c *compositor) string {
	t.Helper()
	// Not t.TempDir: it embeds the test's name, and a unix socket path is
	// capped near 108 bytes, which the names below go past.
	dir, err := os.MkdirTemp("", "wl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 8192)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			c.mu.Lock()
			// A read may carry several requests; each names its own size.
			for b := buf[:n]; len(b) >= 8; {
				packed := order.Uint32(b[4:])
				size := int(packed >> 16)
				if size < 8 || size > len(b) {
					break
				}
				c.handle(order.Uint32(b), uint16(packed), b[8:size])
				b = b[size:]
			}
			out := c.out.Bytes()
			c.out = bytes.Buffer{}
			c.mu.Unlock()
			if len(out) > 0 {
				conn.Write(out)
			}
		}
	}()
	return sock
}

// The success path, which nothing tested before: a compositor that offers the
// input method and has a focused text input takes the string.
//
// This is the regression test for get_registry being sent as a sync. That bug
// left the registry empty, so the manager was never found, so Insert returned
// ErrNoInputMethod on every dictation for the life of the feature.
func TestInsertCommitsToAFocusedTextInput(t *testing.T) {
	c := &compositor{
		offers:  []string{"wl_shm", "wl_seat", "zwp_input_method_manager_v2"},
		focused: true,
	}
	if err := Insert(serve(t, c), "the dictation"); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.text != "the dictation" {
		t.Errorf("committed %q", c.text)
	}
	// The serial must equal the number of done events the object issued, or
	// the compositor takes the text and declines to advance its own state.
	if c.serial != c.dones {
		t.Errorf("commit carried serial %d after %d done events", c.serial, c.dones)
	}
	// Only what the insertion needs, at the version it asks for.
	if len(c.bound) != 2 {
		t.Errorf("bound %v, want the seat and the manager", c.bound)
	}
}

// Ready runs the same handshake and stops before committing, so it must agree
// with Insert about whether a machine can take text.
func TestReadyAgreesWithInsert(t *testing.T) {
	for _, c := range []struct {
		name        string
		offers      []string
		focused     bool
		unavailable bool
		want        error
	}{
		{
			name:    "a focused text input",
			offers:  []string{"wl_seat", "zwp_input_method_manager_v2"},
			focused: true,
		},
		{
			name:    "no input method in the registry, which is KWin and GNOME",
			offers:  []string{"wl_seat", "wl_shm"},
			focused: true,
			want:    ErrNoInputMethod,
		},
		{
			name:   "nothing focused that takes text",
			offers: []string{"wl_seat", "zwp_input_method_manager_v2"},
			want:   ErrNotActive,
		},
		{
			name:        "another input method holds the seat, which is fcitx5 or ibus",
			offers:      []string{"wl_seat", "zwp_input_method_manager_v2"},
			unavailable: true,
			want:        ErrUnavailable,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			// One compositor each: both entry points open a connection and
			// the fake serves one.
			ready := &compositor{offers: c.offers, focused: c.focused, unavailable: c.unavailable}
			if got := Ready(serve(t, ready)); got != c.want {
				t.Errorf("Ready = %v, want %v", got, c.want)
			}
			// Whatever Ready says, Insert must say the same, since Ready
			// exists to report what a dictation would meet.
			insert := &compositor{offers: c.offers, focused: c.focused, unavailable: c.unavailable}
			if got := Insert(serve(t, insert), "text"); got != c.want {
				t.Errorf("Insert = %v, want %v", got, c.want)
			}
		})
	}
}

// Globals is what `diktat doctor` reports, and it read empty for the same
// reason: no registry was ever asked for.
func TestGlobalsListsWhatTheCompositorOffers(t *testing.T) {
	c := &compositor{offers: []string{"wl_shm", "wl_seat", "zwp_input_method_manager_v2"}}
	globals, err := Globals(serve(t, c))
	if err != nil {
		t.Fatal(err)
	}
	if len(globals) != 3 {
		t.Fatalf("got %v, want three globals", globals)
	}
	if !Has(globals, InputMethod) {
		t.Errorf("got %v, want the input method among them", globals)
	}
}
