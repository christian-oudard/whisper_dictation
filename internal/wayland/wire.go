package wayland

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// order is the wire's byte order, which is the host's. Every platform this
// builds for is little-endian, and a big-endian one would need this to follow
// the host rather than the constant.
var order = binary.LittleEndian

// msg is one decoded message. body is the arguments, still encoded, since
// what they mean depends on which object sent it.
type msg struct {
	object uint32
	opcode uint16
	body   []byte
}

// conn is a connection to the compositor. Nothing here tracks object
// lifetimes: every exchange in this package allocates its ids up front from a
// fixed sequence and then throws the connection away, which is what lets a few
// hundred lines stand in for a protocol library.
// Split into the three interfaces rather than held as the socket, so a test
// can drive an exchange from canned events. The state the input method reads
// out of those events is the part of this package most likely to be wrong and
// the part that cannot be exercised without a compositor.
type conn struct {
	r io.Reader
	w io.Writer
	c io.Closer
}

// dial opens the compositor's socket. display is a WAYLAND_DISPLAY value: a
// socket name inside XDG_RUNTIME_DIR, or an absolute path, which is what the
// variable is allowed to hold.
func dial(display string) (*conn, error) {
	if display == "" {
		return nil, errors.New("wayland: WAYLAND_DISPLAY is not set")
	}
	path := display
	if !filepath.IsAbs(path) {
		dir := os.Getenv("XDG_RUNTIME_DIR")
		if dir == "" {
			return nil, errors.New("wayland: XDG_RUNTIME_DIR is not set")
		}
		path = filepath.Join(dir, display)
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	// A compositor that accepts the connection and then says nothing would
	// otherwise hang whatever is waiting on it, which for the input method is
	// a dictation the user is watching for.
	if err := c.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		c.Close()
		return nil, err
	}
	return &conn{r: c, w: c, c: c}, nil
}

func (c *conn) Close() error { return c.c.Close() }

// send writes one message: the target object, then the size and opcode packed
// into a word, then the already-encoded arguments.
func (c *conn) send(object uint32, opcode uint16, args []byte) error {
	size := 8 + len(args)
	b := make([]byte, 8, size)
	order.PutUint32(b[0:], object)
	order.PutUint32(b[4:], uint32(size)<<16|uint32(opcode))
	_, err := c.w.Write(append(b, args...))
	return err
}

func (c *conn) next() (msg, error) { return readMsg(c.r) }

// readMsg decodes one message. It takes a reader rather than the connection so
// the decoding can be tested against bytes.
func readMsg(r io.Reader) (msg, error) {
	var head [8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return msg{}, err
	}
	packed := order.Uint32(head[4:])
	size, opcode := int(packed>>16), uint16(packed)
	if size < 8 {
		return msg{}, fmt.Errorf("wayland: message of %d bytes", size)
	}
	body := make([]byte, size-8)
	if _, err := io.ReadFull(r, body); err != nil {
		return msg{}, err
	}
	return msg{object: order.Uint32(head[0:]), opcode: opcode, body: body}, nil
}

// args builds an argument list. uint, object and new_id are all a bare word on
// the wire, so one method covers the three.
type args []byte

func (a args) uint(v uint32) args {
	b := make([]byte, 4)
	order.PutUint32(b, v)
	return append(a, b...)
}

// str encodes a length counting the terminating NUL, the bytes, then padding
// to the next word.
func (a args) str(s string) args {
	n := len(s) + 1
	a = a.uint(uint32(n))
	a = append(a, s...)
	a = append(a, 0)
	for len(a)%4 != 0 {
		a = append(a, 0)
	}
	return a
}

// takeString decodes a wire string and returns whatever follows it.
func takeString(b []byte) (string, []byte, error) {
	if len(b) < 4 {
		return "", nil, errors.New("wayland: string without a length")
	}
	n := int(order.Uint32(b))
	padded := 4 + (n+3)&^3
	if n < 1 || padded > len(b) {
		return "", nil, fmt.Errorf("wayland: string of %d bytes in %d", n, len(b))
	}
	return string(b[4 : 4+n-1]), b[padded:], nil
}

// protocolError renders wl_display.error, which is how the compositor refuses.
// Its arguments are the object that failed, a code, and the text, and that
// text is the only account of what went wrong.
func protocolError(body []byte) error {
	if len(body) < 8 {
		return errors.New("wayland: protocol error")
	}
	message, _, err := takeString(body[8:])
	if err != nil {
		return errors.New("wayland: protocol error")
	}
	return fmt.Errorf("wayland: protocol error: %s", message)
}
