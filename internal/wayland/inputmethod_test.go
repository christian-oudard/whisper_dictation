package wayland

import (
	"errors"
	"testing"
)

func imEvent(opcode uint16) []byte { return event(methodID, opcode, nil) }

// activate and deactivate are double-buffered: each sets a pending state that
// the next done applies. Reading the flag when it arrives rather than when it
// is applied would report a window as ready to take text before the
// compositor says it is.
func TestInputMethodStateAppliesOnDone(t *testing.T) {
	for _, c := range []struct {
		name   string
		events [][]byte
		active bool
		serial uint32
	}{
		{
			"activated and applied",
			[][]byte{imEvent(imActivate), imEvent(imDone)},
			true, 1,
		},
		{
			"activated but not yet applied",
			[][]byte{imEvent(imActivate)},
			false, 0,
		},
		{
			"deactivated",
			[][]byte{imEvent(imActivate), imEvent(imDone), imEvent(imDeactivate), imEvent(imDone)},
			false, 2,
		},
		{
			"nothing said at all, which is a compositor with no focused text input",
			nil,
			false, 0,
		},
		{
			// The surrounding text and content type of the focused field are
			// no business of a dictation, but they arrive between activate and
			// done and must not disturb the count.
			"state events between activate and done",
			[][]byte{
				imEvent(imActivate),
				event(methodID, imSurroundingText, args{}.str("existing text").uint(0).uint(0)),
				event(methodID, imContentType, args{}.uint(0).uint(0)),
				imEvent(imDone),
			},
			true, 1,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			conn, _ := replay(append(c.events, done(activeSyncID))...)
			active, serial, err := conn.inputMethodState(activeSyncID)
			if err != nil {
				t.Fatal(err)
			}
			if active != c.active {
				t.Errorf("active = %v, want %v", active, c.active)
			}
			// The serial a commit carries must equal the number of done
			// events the object issued, or the compositor takes the text and
			// declines to advance its own state.
			if serial != c.serial {
				t.Errorf("serial = %d, want %d", serial, c.serial)
			}
		})
	}
}

// Sent as the only event on the object when something else had the seat
// first, which is what happens on a machine running fcitx5 or ibus. Falling
// back to typing is right; reporting a failure is not.
func TestInputMethodStateReportsAnotherInputMethod(t *testing.T) {
	conn, _ := replay(imEvent(imUnavailable))
	_, _, err := conn.inputMethodState(activeSyncID)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !Unavailable(err) {
		t.Error("Unavailable says this is a failure rather than a reason to type instead")
	}
}

// The three reasons to type instead are states of a working machine. Anything
// else is a fault and must not be swallowed as one of them.
func TestUnavailableCoversOnlyTheFallbackReasons(t *testing.T) {
	for _, err := range []error{ErrNoInputMethod, ErrUnavailable, ErrNotActive} {
		if !Unavailable(err) {
			t.Errorf("%v should be a reason to type instead", err)
		}
	}
	if Unavailable(errors.New("the socket went away")) {
		t.Error("an unrelated failure should not read as a reason to type instead")
	}
}
