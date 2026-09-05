# Speaking Wayland

`internal/wayland` is a few hundred lines of wire protocol rather than a
binding to libwayland. This is why, what it cost, and what would change the
answer.


## What broke, and what it was really about

`wl_display.get_registry` was written as a bare name under a literal:

```go
const (
    displaySync = 0
    displayGetRegistry      // repeats the literal, so also 0
)
```

A bare name in a Go const block repeats the previous *expression*. Under
`iota` that counts; under `0` it repeats `0`. So every registry request diktat
ever sent was a second `sync`. sway answered exactly as asked -- a callback,
its `done`, then `delete_id` -- and the registry listing came back empty. The
input method was never once reached, from the day it was written until
`7d4f0a2`, and `diktat doctor` reported no protocols for the same reason.

The lesson is not that hand-writing the wire was wrong. The framing, the
string padding and the `bind` special case were all correct and all tested.
What failed was two integers transcribed by hand, and they were the integers
for `wl_display` -- the most ordinary interface in the protocol, the part any
generated binding gets right for free. The exotic protocol was not the risk.

It survived six wire tests because every fake was built from this package's
own constants: `globalEvent` sent `registryGlobalEvent`, `replay` keyed on
`registryID`. A wrong constant moved both sides together. The rule that
follows is in `compositor_test.go` and is the whole reason it exists: **at a
protocol boundary, assert against the numbers the specification declares, not
against your own identifiers.** Named constants inside the package; literals,
with a citation, at the wire.


## The XML is not packaged

Generating the constants is the fix that removes the class, and it needs the
protocol definitions. Checked, on a machine with nixpkgs:

- `wayland-protocols` 1.48 ships `unstable/input-method/input-method-unstable-v1.xml`,
  and an experimental `xx-input-method-v2`, which is a later redesign and a
  different protocol.
- `wlr-protocols` in nixpkgs ships ten protocols and no input method at all.
- The `wlroots` output installs headers and a shared library; the XML it was
  built from is not among them.

So `input-method-unstable-v2.xml`, which is what sway implements and what this
package speaks, is in the wlroots source tree and in no package. Core
`wayland.xml` is likewise absent from the `wayland` output, which is libraries
only. Either route -- generator or third-party binding -- means vendoring the
XML.

No Go package ships the protocol either. `go-wayland` has input-method-v1;
`libwldevices-go` has virtual pointer and keyboard. Both ship a scanner and
leave v2 to the caller.


## Why not libwayland

Not because the wire is hard. Because the shape is wrong.

libwayland and the Go bindings modelled on it manage object lifetimes, a
free-list of ids, and a dispatch loop delivering events to registered
listeners. That is what a client with surfaces and frame callbacks needs. This
package opens a connection, performs one exchange, and closes it. It allocates
its ids from a fixed sequence because the exchange is short enough to count
them by hand, and it blocks on a roundtrip because there is nothing else to do
meanwhile.

What would suit it is a *codec* rather than a *client*: generated encoding and
decoding, no object registry, no dispatch. The split worth building is
**generated marshalling, hand-written session** -- generate the mechanical
part that broke, keep the session logic, which is specific to committing once
and going away.

That trade turns over if the protocol count grows. KWin needs
`input-method-v1`, and knowing whether a field is focused before dictating
would want `text-input-v3`. At three or four interfaces of hand-typed numbers,
a generator or a binding wins outright.


## The connection is not held

Opened per insertion and closed after. The reason recorded in the code is that
there may be no more than one input method per seat, so holding it would lock
fcitx5 and ibus out for the session.

That reason is true but it is not the binding one, and it names the wrong
object. The seat is claimed by `get_input_method`, not by the connection, the
registry or the manager binding. Those three could be held while the input
method stayed per-insertion, and nothing would be locked out.

It is not worth doing. Measured by `BenchmarkInsert` against the fake, on an
Intel Ultra 7 165H:

| | per operation |
|---|---|
| a whole insertion | 62 us |
| the handshake alone | 50 us |
| the registry listing alone | 37 us |

A dictation costs a few hundred milliseconds to transcribe, so the handshake
is under a thousandth of it, and holding the connection could save at most the
50 us. Against that, a per-insertion connection re-resolves `WAYLAND_DISPLAY`
every time and so survives a compositor restart for nothing, which is the
whole subject of `internal/output/env.go`; a held one would need to notice the
socket dying and reconnect.

The numbers exclude the compositor's own work and come from a fake with four
globals where a real session has many more. They are a floor. They are three
orders of magnitude from mattering, which is the point.
