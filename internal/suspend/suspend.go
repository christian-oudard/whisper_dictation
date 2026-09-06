// Package suspend reports how long this machine has spent suspended, which is
// how the daemon notices a resume it slept through.
//
// The kernel keeps two monotonic clocks: CLOCK_MONOTONIC stops while the
// machine is suspended and CLOCK_BOOTTIME does not, so their difference is a
// ledger of sleep. It moves only when the machine sleeps, on suspend-to-RAM,
// s2idle and hibernation alike, and nothing else touches it: NTP slews both
// clocks together, so unlike the wall clock it cannot drift or be corrected.
// A caller that remembers the last reading and sees a larger one knows the
// machine slept in between, and by how long.
package suspend

import (
	"syscall"
	"time"
	"unsafe"
)

// From linux/time.h. CLOCK_MONOTONIC is in the syscall package under another
// build tag but not this one, so both are spelled out.
const (
	clockMonotonic = 1
	clockBoottime  = 7
)

func read(clock uintptr) time.Duration {
	var ts syscall.Timespec
	// Cannot fail for these clocks: both exist on every kernel this runs on,
	// and the timespec is on our stack.
	syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clock, uintptr(unsafe.Pointer(&ts)), 0)
	return time.Duration(ts.Nano())
}

// Total is the time this machine has spent suspended since boot. The two
// clocks are read one syscall apart, so it jitters by microseconds between
// calls; a suspend is seconds at the least. Monotonic is read first so the
// gap between the reads biases the result positive: read the other way, a
// machine that has never suspended reports a small negative total.
func Total() time.Duration {
	m := read(clockMonotonic)
	return read(clockBoottime) - m
}
