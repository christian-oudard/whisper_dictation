package wayland

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// event builds what a compositor sends, which is the same framing as a
// request. Kept separate from conn.send so a mistake in one does not cancel
// out against the same mistake in the other.
func event(object uint32, opcode uint16, body []byte) []byte {
	size := 8 + len(body)
	head := make([]byte, 8)
	order.PutUint32(head[0:], object)
	order.PutUint32(head[4:], uint32(size)<<16|uint32(opcode))
	return append(head, body...)
}

// replay is a conn that reads canned events and remembers what was written to
// it, which is as much of a compositor as any of this needs.
func replay(events ...[]byte) (*conn, *bytes.Buffer) {
	var sent bytes.Buffer
	return &conn{r: bytes.NewReader(bytes.Join(events, nil)), w: &sent, c: io.NopCloser(nil)}, &sent
}

func globalEvent(name uint32, iface string, version uint32) []byte {
	return event(registryID, registryGlobalEvent,
		args{}.uint(name).str(iface).uint(version))
}

func done(object uint32) []byte { return event(object, callbackDoneEvent, nil) }

// Strings carry a length that counts the NUL and are padded to a word, which
// is where a hand-written codec goes wrong. These names land on either side of
// a boundary: "wl_seat" is 7 bytes and 8 with its NUL, "wl_shm" is 6 and 7.
func TestStringsSurviveTheRoundTrip(t *testing.T) {
	for _, s := range []string{"", "a", "wl_shm", "wl_seat", "zwp_input_method_manager_v2"} {
		encoded := args{}.str(s)
		if len(encoded)%4 != 0 {
			t.Errorf("%q encodes to %d bytes, which is not a whole number of words", s, len(encoded))
		}
		got, rest, err := takeString(encoded)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if got != s {
			t.Errorf("got %q, want %q", got, s)
		}
		if len(rest) != 0 {
			t.Errorf("%q left %d bytes over", s, len(rest))
		}
	}
}

// bind is the one request whose new_id is sent as an interface and version
// before the id, because the protocol names no interface for it. Getting this
// wrong desynchronises everything after it.
func TestBindSendsTheInterfaceBeforeTheID(t *testing.T) {
	c, sent := replay()
	if err := c.bind(Global{Name: 7, Interface: "wl_seat", Version: 9}, 1, seatID); err != nil {
		t.Fatal(err)
	}
	m, err := readMsg(sent)
	if err != nil {
		t.Fatal(err)
	}
	if m.object != registryID || m.opcode != registryBind {
		t.Fatalf("sent object %d opcode %d, want the registry's bind", m.object, m.opcode)
	}
	if got := order.Uint32(m.body); got != 7 {
		t.Errorf("name = %d, want the global's own number 7", got)
	}
	iface, rest, err := takeString(m.body[4:])
	if err != nil {
		t.Fatal(err)
	}
	if iface != "wl_seat" {
		t.Errorf("interface = %q", iface)
	}
	if version := order.Uint32(rest); version != 1 {
		t.Errorf("version = %d, want the version asked for, not the one advertised", version)
	}
	if id := order.Uint32(rest[4:]); id != seatID {
		t.Errorf("id = %d, want %d", id, seatID)
	}
}

func TestGlobalsReadsToTheSyncCallback(t *testing.T) {
	c, _ := replay(
		globalEvent(1, "wl_seat", 9),
		globalEvent(2, InputMethod, 1),
		// Bookkeeping for an object this never made, which must be skipped
		// rather than parsed.
		event(displayID, 1, args{}.uint(0)),
		done(globalsSyncID),
		// Anything after the callback is not ours to read.
		globalEvent(3, "never_read", 1),
	)
	globals, err := c.globals(globalsSyncID)
	if err != nil {
		t.Fatal(err)
	}
	if len(globals) != 2 {
		t.Fatalf("got %v, want two globals", globals)
	}
	if !Has(globals, InputMethod) || Has(globals, "never_read") {
		t.Errorf("got %v", globals)
	}
	if g, _ := find(globals, InputMethod); g.Name != 2 {
		t.Errorf("name = %d, want the compositor's handle 2", g.Name)
	}
}

func TestReadMsgRejectsATruncatedMessage(t *testing.T) {
	for _, c := range []struct {
		name string
		msg  []byte
	}{
		{"body shorter than the size says", event(registryID, 0, args{}.uint(1))[:10]},
		{"size below the header", func() []byte {
			b := event(registryID, 0, nil)
			order.PutUint32(b[4:], uint32(4)<<16)
			return b
		}()},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := readMsg(bytes.NewReader(c.msg)); err == nil {
				t.Error("no error")
			}
		})
	}
}

// A global with a well-formed header and nothing in it once walked off the end
// of a slice. Parsing bytes from another process must fail rather than panic.
func TestGlobalsRejectsAnEmptyGlobal(t *testing.T) {
	c, _ := replay(event(registryID, registryGlobalEvent, nil), done(globalsSyncID))
	if _, err := c.globals(globalsSyncID); err == nil {
		t.Error("no error")
	}
}

// A global that fails to decode has been seen in the field, from a frame this
// package could not reproduce. The error is the bug report, so it must carry
// the bytes the compositor sent, not only what was missing from them.
func TestGlobalsNamesTheBadFrame(t *testing.T) {
	c, _ := replay(event(registryID, registryGlobalEvent, args{}.uint(33)), done(globalsSyncID))
	_, err := c.globals(globalsSyncID)
	if err == nil || !strings.Contains(err.Error(), "21 00 00 00") {
		t.Errorf("err = %v, want the frame's own bytes in it", err)
	}
}

func TestGlobalsReportsAProtocolError(t *testing.T) {
	body := args{}.uint(displayID).uint(1).str("invalid method")
	c, _ := replay(event(displayID, displayErrorEvent, body))
	_, err := c.globals(globalsSyncID)
	if err == nil || !strings.Contains(err.Error(), "invalid method") {
		t.Errorf("err = %v, want the compositor's message", err)
	}
}
