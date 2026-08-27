// Package sco reports whether the bluetooth adapters hold a synchronous audio
// connection, which is the link a headset's microphone arrives on.
//
// It exists because a dead one is invisible everywhere else. The PipeWire
// source stays RUNNING and unmuted and delivers bit-exact zero, its
// bluez5.profile property reads "off" whether the link is alive or not, and
// the kernel's one log line is emitted by ordinary teardown as well as by
// failure. The connection list is the state itself rather than a symptom of
// it: the kernel's complaint when the link broke was that the handle was
// unknown to it, so the handle is simply gone from here.
//
// This is the same HCIGETCONNLIST ioctl `hcitool con` uses, done directly
// because hcitool is deprecated upstream and ships disabled in some builds. A
// dependency exercised only in a rare failure path would fail silently.
package sco

import (
	"encoding/binary"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"syscall"
	"unsafe"
)

const (
	btprotoHCI = 1
	// _IOR('H', 212, int), from bluez's hci.h.
	hciGetConnList = 0x800448d4

	// Link types. A headset microphone is carried by one of the synchronous
	// two; the asynchronous one carries everything else, control included, and
	// survives the failure this package is looking for.
	scoLink  = 0x00
	aclLink  = 0x01
	escoLink = 0x02

	// connInfoSize is sizeof(struct hci_conn_info): handle, bdaddr, type, out,
	// state, link_mode.
	connInfoSize = 2 + 6 + 1 + 1 + 2 + 4
	// maxConns bounds one adapter's list. Far past the seven a bluetooth
	// piconet allows.
	maxConns = 64
)

var adapterName = regexp.MustCompile(`^hci([0-9]+)$`)

// Links reports how many synchronous audio connections exist across every
// adapter. One means a headset microphone has a live link; zero while a
// headset is meant to be in its headset profile means the link is gone and
// the audio device has to be rebuilt.
//
// Zero is also the answer on a machine with no bluetooth at all, which is why
// callers watch for the transition rather than the value.
func Links() (int, error) {
	ids, err := adapters()
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	fd, err := syscall.Socket(syscall.AF_BLUETOOTH, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, btprotoHCI)
	if err != nil {
		return 0, fmt.Errorf("open hci socket: %w", err)
	}
	defer syscall.Close(fd)

	return totalLinks(ids, func(id int) (int, error) { return adapterLinks(fd, id) })
}

// totalLinks adds up what every adapter reports, tolerating an adapter that
// does not answer as long as another one does.
//
// One adapter failing is ordinary: a USB dongle can be pulled between listing
// the adapters and asking this one, and its ioctl then fails with the rest of
// the machine's bluetooth working. The caller stops watching for good on an
// error, so failing the whole call for that would silently retire the watch
// on exactly the machines that have a headset to lose.
//
// Every adapter failing is different. That is no permission, or no bluetooth
// stack, and the caller should hear about it once rather than be told there
// are no links -- which is what would make it rebuild the audio device for
// nothing.
func totalLinks(ids []int, count func(int) (int, error)) (int, error) {
	total, answered := 0, 0
	var first error
	for _, id := range ids {
		n, err := count(id)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		answered++
		total += n
	}
	if answered == 0 && first != nil {
		return 0, first
	}
	return total, nil
}

// adapters lists the adapter numbers. The connection entries sit in the same
// directory as hci0:256, so the pattern anchors on a name that is all digits
// after the prefix.
func adapters() ([]int, error) {
	entries, err := os.ReadDir("/sys/class/bluetooth")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, e := range entries {
		m := adapterName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		id, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// adapterLinks counts one adapter's synchronous connections. The request is a
// struct hci_conn_list_req: the device and how many entries there is room
// for going in, the number filled coming back.
func adapterLinks(fd, id int) (int, error) {
	buf := make([]byte, 4+maxConns*connInfoSize)
	binary.NativeEndian.PutUint16(buf[0:], uint16(id))
	binary.NativeEndian.PutUint16(buf[2:], maxConns)

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), hciGetConnList, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return 0, fmt.Errorf("hci conn list for hci%d: %w", id, errno)
	}

	return countSync(buf, min(int(binary.NativeEndian.Uint16(buf[2:])), maxConns)), nil
}

// countSync counts the synchronous links among the first n entries of a
// filled hci_conn_list_req. Split out because it is all struct arithmetic,
// which is the half of this that can be wrong without the ioctl failing.
func countSync(buf []byte, n int) int {
	count := 0
	for i := range n {
		// handle and bdaddr come before the type byte.
		switch buf[4+i*connInfoSize+8] {
		case scoLink, escoLink:
			count++
		}
	}
	return count
}
