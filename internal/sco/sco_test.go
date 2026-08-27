package sco

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// conn writes one struct hci_conn_info at entry i.
func conn(buf []byte, i int, handle uint16, linkType byte) {
	off := 4 + i*connInfoSize
	binary.NativeEndian.PutUint16(buf[off:], handle)
	// bdaddr occupies the next six bytes and is left zero.
	buf[off+8] = linkType
}

// The layout is the part that can be wrong while the ioctl still succeeds: a
// type byte read at the wrong offset counts whatever happens to sit there.
func TestCountSync(t *testing.T) {
	buf := make([]byte, 4+maxConns*connInfoSize)
	// What the adapter reports with a headset on a call: the control link and
	// the audio link that carries the microphone.
	conn(buf, 0, 256, aclLink)
	conn(buf, 1, 257, escoLink)
	if got := countSync(buf, 2); got != 1 {
		t.Errorf("countSync(acl+esco) = %d, want 1", got)
	}
	// The same headset with its audio link gone, which is the failure: the
	// control link is still there, so the device still looks connected
	// everywhere that only asks whether it is connected.
	if got := countSync(buf, 1); got != 0 {
		t.Errorf("countSync(acl only) = %d, want 0", got)
	}
	// Older headsets negotiate SCO rather than eSCO, and a mouse and keyboard
	// alongside must not read as microphones.
	conn(buf, 1, 257, aclLink)
	conn(buf, 2, 258, aclLink)
	conn(buf, 3, 259, scoLink)
	if got := countSync(buf, 4); got != 1 {
		t.Errorf("countSync(three acl + sco) = %d, want 1", got)
	}
}

// Against the real adapter. Skipped where there is none, so it does not turn
// a laptop with the bluetooth off into a failing build.
func TestLinks(t *testing.T) {
	if _, err := os.Stat("/sys/class/bluetooth"); err != nil {
		t.Skip("no bluetooth on this machine")
	}
	n, err := Links()
	if err != nil {
		t.Fatalf("Links() error: %v", err)
	}
	if n < 0 {
		t.Errorf("Links() = %d", n)
	}
	t.Logf("synchronous links now: %d", n)
}

// A dongle pulled between listing the adapters and asking one of them is
// ordinary, and the caller stops watching for good on an error: failing the
// whole call would retire the watch on exactly the machines that have a
// headset to lose.
func TestOneAdapterMayFail(t *testing.T) {
	total, err := totalLinks([]int{0, 1}, func(id int) (int, error) {
		if id == 1 {
			return 0, errors.New("no such device")
		}
		return 2, nil
	})
	if err != nil || total != 2 {
		t.Errorf("totalLinks = %d, %v; want the working adapter's 2 and no error", total, err)
	}
}

// Every adapter failing is not one dongle, it is no permission or no stack,
// and the caller has to hear about it rather than be told there are no links.
func TestEveryAdapterFailing(t *testing.T) {
	if _, err := totalLinks([]int{0, 1}, func(int) (int, error) {
		return 0, errors.New("operation not permitted")
	}); err == nil {
		t.Error("totalLinks reported no links when nothing could be read")
	}
}
