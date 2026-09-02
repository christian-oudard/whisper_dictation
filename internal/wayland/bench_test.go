package wayland

import "testing"

// What one insertion costs on this side of the socket: connect, enumerate the
// globals, bind two of them, claim the input method, commit, close.
//
// Against the fake, so it is a floor rather than a measurement of a real
// desktop -- sway's own work is not in it. What it does capture is the part
// that would be saved by holding the connection open: the connect and the
// registry enumeration.
func BenchmarkInsert(b *testing.B) {
	c := &compositor{
		offers:  []string{"wl_shm", "wl_seat", "wl_output", "zwp_input_method_manager_v2"},
		focused: true,
	}
	display := serve(b, c)
	b.ResetTimer()
	for range b.N {
		if err := Insert(display, "the dictation"); err != nil {
			b.Fatal(err)
		}
	}
}

// The handshake alone, without the commit: what a held connection would skip.
func BenchmarkHandshake(b *testing.B) {
	c := &compositor{
		offers:  []string{"wl_shm", "wl_seat", "wl_output", "zwp_input_method_manager_v2"},
		focused: true,
	}
	display := serve(b, c)
	b.ResetTimer()
	for range b.N {
		if err := Ready(display); err != nil {
			b.Fatal(err)
		}
	}
}

// Just the registry: the part a held connection caches outright.
func BenchmarkGlobals(b *testing.B) {
	c := &compositor{offers: []string{"wl_shm", "wl_seat", "wl_output", "zwp_input_method_manager_v2"}}
	display := serve(b, c)
	b.ResetTimer()
	for range b.N {
		if _, err := Globals(display); err != nil {
			b.Fatal(err)
		}
	}
}
