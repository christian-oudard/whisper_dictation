package wayland

import (
	"errors"
)

// The input method is the only way to put text into a window that does not
// pretend to be a keyboard. wtype and uinput synthesise a key press and
// release per character, and an application that runs its full input handling
// on each one is slow in proportion to what was dictated; commit_string is one
// message carrying the whole string, inserted through the application's own
// text input path, so undo and input handling see it as text rather than as
// typing.
//
// The connection is opened per insertion and closed after, rather than held
// for the session. There may be no more than one input method per seat, so
// holding it would lock out fcitx5, ibus and anything else the user runs on
// purpose, for the sake of a socket that costs under a millisecond to open.

// Errors that mean fall back to typing rather than give up. Each is a normal
// state of a working machine, not a fault: the compositor may not implement
// the protocol at all, another input method may hold the seat, or the window
// in front of the user may have nowhere to put text.
var (
	ErrNoInputMethod = errors.New("wayland: the compositor offers no input method")
	ErrUnavailable   = errors.New("wayland: another input method holds this seat")
	ErrNotActive     = errors.New("wayland: the focused window has no text input")
)

// InputMethod is the wlroots protocol, which sway, Hyprland, river and
// Wayfire have. KWin implements the older input-method-v1 instead and GNOME
// implements neither, so this name being absent is the common case rather
// than a broken machine.
const InputMethod = "zwp_input_method_manager_v2"

// zwp_input_method_v2 events, in the order the protocol declares them.
const (
	imActivate = iota
	imDeactivate
	imSurroundingText
	imTextChangeCause
	imContentType
	imDone
	imUnavailable
)

// zwp_input_method_v2 requests, likewise. Only the two are used: everything
// between them is preedit and surrounding-text bookkeeping that a dictation
// has no opinion about.
const (
	imCommitString = 0
	imCommit       = 3
)

// zwp_input_method_manager_v2.get_input_method.
const managerGetInputMethod = 0

// The ids this exchange spends, after the display and registry.
const (
	globalsSyncID = firstFreeID + iota
	seatID
	managerID
	methodID
	activeSyncID
	commitSyncID
)

// Insert commits text to whatever holds keyboard focus, and reports whether
// the compositor accepted it.
//
// It returns one of the errors above when the mechanism is unavailable, which
// is the caller's signal to type instead. Anything else is a real failure of
// a mechanism that should have worked.
func Insert(display, text string) error {
	c, serial, err := openInputMethod(display)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.send(methodID, imCommitString, args{}.str(text)); err != nil {
		return err
	}
	// The serial must equal the number of done events the object has issued,
	// or the compositor applies the text and then declines to advance its own
	// state. Counting them is the whole of the bookkeeping.
	if err := c.send(methodID, imCommit, args{}.uint(serial)); err != nil {
		return err
	}
	// Wait for the compositor to work through both before dropping the
	// connection, rather than trusting a write to have arrived.
	return c.roundtrip(commitSyncID)
}

// Ready reports whether an insertion would land right now. It runs the same
// handshake as Insert and stops before committing anything, so what it
// answers is what a dictation would meet, and it is for the doctor report
// rather than for deciding anything: Insert says the same thing by returning.
func Ready(display string) error {
	c, _, err := openInputMethod(display)
	if err != nil {
		return err
	}
	return c.Close()
}

// openInputMethod connects, claims the seat's input method, and reports the
// serial an insertion has to carry. Every reason this cannot be done is one
// of the errors above.
func openInputMethod(display string) (*conn, uint32, error) {
	c, err := dial(display)
	if err != nil {
		return nil, 0, err
	}
	closing := func(err error) (*conn, uint32, error) {
		c.Close()
		return nil, 0, err
	}

	globals, err := c.globals(globalsSyncID)
	if err != nil {
		return closing(err)
	}
	manager, ok := find(globals, InputMethod)
	if !ok {
		return closing(ErrNoInputMethod)
	}
	seat, ok := find(globals, "wl_seat")
	if !ok {
		return closing(errors.New("wayland: the compositor offers no seat"))
	}

	// Version 1 of each, since nothing here uses anything a later one added.
	if err := c.bind(seat, 1, seatID); err != nil {
		return closing(err)
	}
	if err := c.bind(manager, 1, managerID); err != nil {
		return closing(err)
	}
	if err := c.send(managerID, managerGetInputMethod,
		args{}.uint(seatID).uint(methodID)); err != nil {
		return closing(err)
	}

	// Whether a text input is focused is not something to ask for: the
	// compositor volunteers activate or deactivate, and a sync is how we learn
	// it has finished telling us.
	active, serial, err := c.inputMethodState(activeSyncID)
	if err != nil {
		return closing(err)
	}
	if !active {
		return closing(ErrNotActive)
	}
	return c, serial, nil
}

// inputMethodState reads up to the sync callback and reports whether a text
// input is focused, and how many done events arrived, which is the serial a
// commit has to carry.
//
// activate and deactivate are double-buffered: each sets a pending state that
// the next done applies. So the flag is only read out at a done, and what is
// pending in between is not yet true.
func (c *conn) inputMethodState(sync uint32) (bool, uint32, error) {
	if err := c.send(displayID, displaySync, args{}.uint(sync)); err != nil {
		return false, 0, err
	}
	var active, pending bool
	var serial uint32
	for {
		m, err := c.next()
		if err != nil {
			return false, 0, err
		}
		switch {
		case m.object == displayID && m.opcode == displayErrorEvent:
			return false, 0, protocolError(m.body)
		case m.object == methodID:
			switch m.opcode {
			case imActivate:
				pending = true
			case imDeactivate:
				pending = false
			case imDone:
				active = pending
				serial++
			case imUnavailable:
				// Sent as the only event on the object when someone else had
				// the seat first. Nothing after it is worth reading.
				return false, 0, ErrUnavailable
			}
		case m.object == sync && m.opcode == callbackDoneEvent:
			return active, serial, nil
		}
	}
}

// roundtrip waits for the compositor to reach everything sent before it.
func (c *conn) roundtrip(sync uint32) error {
	if err := c.send(displayID, displaySync, args{}.uint(sync)); err != nil {
		return err
	}
	for {
		m, err := c.next()
		if err != nil {
			return err
		}
		switch {
		case m.object == displayID && m.opcode == displayErrorEvent:
			return protocolError(m.body)
		case m.object == sync && m.opcode == callbackDoneEvent:
			return nil
		}
	}
}

// Unavailable reports whether err is one of the reasons to type instead,
// rather than a failure to report.
func Unavailable(err error) bool {
	return errors.Is(err, ErrNoInputMethod) ||
		errors.Is(err, ErrUnavailable) ||
		errors.Is(err, ErrNotActive)
}
