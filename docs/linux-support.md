# Supporting the Linux desktop: a proposal

Diktat runs on sway and nowhere else. Not because of the model, the audio or
the daemon, which are all indifferent to the compositor, but because three
places assume sway specifically. This is what covering the rest of the Linux
desktop costs, in what order, and which parts of it cannot be covered at all.

Every protocol claim here was read off the implementation rather than
recalled, and several early guesses did not survive that. What is built so far
is the input method in `internal/wayland` and `diktat doctor`.


## What already ports

- **Audio.** malgo is miniaudio, which speaks PulseAudio and ALSA. PipeWire is
  reached through its Pulse shim; miniaudio has no native PipeWire backend, so
  a machine with PipeWire but no `pipewire-pulse` has no capture.
- **The bluetooth rebuild.** `internal/sco` asks the kernel for the adapters'
  connection list, which is BlueZ on any distribution.
- **GPU.** Vulkan covers Mesa and the NVIDIA driver, with CPU as the fallback.
- **Activation.** The compositor binds a key to `diktat toggle`, which sends a
  signal. Every desktop can bind a key to a command, so this is already the
  portable half and should not change.
- **Everything above the output layer:** `asr`, `warmup`, `audio`, `models`,
  `config`, `ipc`, `xdg`.


## What is bound to sway

**1. Finding the session.** `internal/output/env.go` locates
`sway-ipc.<uid>.<pid>.sock` and reads `WAYLAND_DISPLAY` out of that process's
`/proc/<pid>/environ`. Under systemd the daemon inherits neither variable, so
on Hyprland, KDE or GNOME this lookup fails and `output.Type` returns an error
before any of the rest is reached. Diktat types nothing at all there today.
This blocks everything else and is the smallest of the three.

**2. wtype**, which drives `zwp_virtual_keyboard_v1`, a wlroots protocol
rather than a freedesktop one.

**3. The focused window.** `focusedAppID` shells out to `swaymsg -t get_tree`.
Its only caller is `Type`, and its only purpose is the `typing_methods`
lookup.


## Getting text into a window

Six mechanisms exist. They divide by whether they hand over the whole string
or pretend to be a keyboard, and that division is the whole design: only the
first kind is fast, and the reason `typing_methods` exists at all is that the
second kind is slow in an application that runs its input handling per
keystroke.

### Whole string, one call

**`zwp_input_method_v2`** — wlroots only, and *built*. There is no v3 and no
freedesktop v2: `wayland-protocols` carries only `input-method-unstable-v1`,
and v2 belongs to wlroots. The other two compositors differ in architecture
rather than version. `KWin::InputMethod` *launches* its input method:
`createInputMethodConnection` makes a socketpair, the fd goes to a `QProcess`
as `WAYLAND_SOCKET`, and `zwp_input_method_v1` is exposed only on that private
connection, with the command coming from Plasma's "Virtual Keyboard" setting.
Mutter has no input-method protocol at all; gnome-shell embeds ibus and *is*
the input method. So on wlroots an input method is a peer, on KWin it is a
child, and on GNOME there is no such role to fill.

**AT-SPI `EditableText.InsertText`** — every desktop, every display server,
toolkit text fields only. A plain method call on the focused accessible
widget, with no engine to become and no seat to claim. This is the only fast
mechanism that reaches GNOME and KDE.

**ibus is ruled out.** On `org.freedesktop.IBus.InputContext`, `CommitText` is
a *signal*: text travels engine to ibus to client and nothing outside can
inject into that path. `SetGlobalEngine` would let diktat register an engine,
switch the user's selection to it, commit, and switch back, which is the
exclusivity problem again, global rather than per seat and visible in the
panel.

### Keystrokes

**wtype** — wlroots. Present already.

**The RemoteDesktop portal** — GNOME and KDE, and the reason this design does
not need a udev rule on either. `NotifyKeyboardKeysym` injects a key, and
version 2 adds `ConnectToEIS`, which hands back an fd for a libei sender
context so events go down a socket rather than one D-Bus round trip per
character. It needs no privileges at all, only consent, and `persist_mode`
value 2 — "permissions persist until explicitly revoked" — with the returned
`restore_token` means that consent is asked once and never again.

The name is GNOME's rather than the portal's, and it is not a misuse. Mutter
exposes no Wayland protocol for typing, deliberately: a client that could bind
one could type into anything. `org.gnome.Mutter.RemoteDesktop` is the whole of
its input emulation API, the portal is the cross-desktop wrapper that adds
consent in front of it, and Mutter's own tests drive the same interface.

Its `Session` also carries `SetKeymap`, which takes an XKB keymap over a file
descriptor and can lock it. So arbitrary text is typeable without depending on
the user's layout: build a keymap holding exactly the keysyms a transcript
needs, install it, press the keys. That is the technique `wtype` already uses
on wlroots, and it removes the one unknown that could have made this phase
impossible rather than merely long.

**uinput** — everything, including a bare TTY, and the only mechanism that
works with no display server whatsoever. The cost is the permission:
`/dev/uinput` needs a udev rule and a group, which is a packaging burden per
distribution and the thing the portal exists to avoid.

**xdotool** — X11.

### The clipboard

Not a wart. Pasting is what a terminal wants: AT-SPI cannot reach one, because
a pty is not an editable widget, so off wlroots a terminal has no whole-string
mechanism at all — and a terminal is also exactly the application with a
well-known paste chord. `typing_methods` stops being a workaround for slow
applications in general and becomes the answer for the one class that nothing
else covers.

Setting the clipboard is not free everywhere, though. `wl-copy` needs a
data-control protocol, and Mutter implements none: across its whole tree there
is no `wlr-data-control` and no `ext-data-control`, only `wl_data_device` for
focused clients. So on GNOME the shell tools cannot set the selection at all,
and the clipboard is reached through `org.freedesktop.portal.Clipboard`
instead, which is unlocked by asking for it on a RemoteDesktop session.

That portal is better than the tools it replaces, not worse. `SetSelection`
publishes, `SelectionWrite` serves, and `SelectionTransfer` fires when a
client actually reads — which is the receipt `wl-copy` cannot give, and the
thing that would make restoring the previous clipboard correct rather than
merely quick.


## What each desktop ends up with

| desktop | fast path | keystrokes | terminal |
|---|---|---|---|
| sway, Hyprland, wlroots | input method | wtype | input method |
| KDE | AT-SPI | portal / libei | clipboard |
| GNOME | AT-SPI | portal / libei | clipboard |
| X11, any WM | AT-SPI | xdotool | clipboard |
| TTY, no session | — | uinput | uinput |

No distribution appears in that table, which is the point: what varies is the
compositor and the toolkit, and a distribution only decides which of those is
installed.


## The shape in code

`internal/output` becomes a set of backends behind one question — put this
string into the focused window — and a chooser that probes once at startup and
falls through per insertion:

    type Inserter interface {
        Insert(text string) error
        Name() string
    }

Falling through has to be per insertion rather than decided once, because
every fast mechanism answers *for the window in front of the user right now*.
The input method reports whether the focused surface enabled a text input;
AT-SPI reports whether the focused accessible implements `EditableText`.
Neither is a property of the machine, and a chooser that cached one would be
wrong the moment focus moved.


## Phases

Ordered for Ubuntu, which is GNOME, which is the desktop where diktat has no
working mechanism at all. It is both the largest target and the hardest, so
everything that only helps elsewhere waits.

**0. `diktat doctor`. Done.** Report the session, the advertised protocols,
the helper binaries, whether `/dev/uinput` opens, and what the compute
backends found. Supporting people on machines nobody here runs means reading
their bug reports, and this is what makes those answerable.

**1. Stop requiring sway.** `waylandEnv` returns early only when `SWAYSOCK`
*and* `WAYLAND_DISPLAY` are set, so on any other compositor it falls through
to `swaySocket` and fails — even on GNOME, which puts `WAYLAND_DISPLAY` into
the systemd user environment, so the daemon inherited it and the value was
there all along. `SWAYSOCK` is needed only for `swaymsg`, which is needed only
for `typing_methods`. Making it optional rather than required is a few lines
and turns "fails immediately" into "runs, and reports what it cannot do",
which is also what makes a doctor report from those users worth reading.

Discovering the display on a compositor that does *not* pre-set it — sway
started from a TTY is the case — still wants generalising: find the live
`wayland-N` socket and resolve its owning process through `/proc/net/unix` and
`/proc/*/fd`, keeping the property the current code was written for, that the
value comes from the compositor actually running rather than from a name a
greeter also leaves behind. That half is not on Ubuntu's path and can follow.

**2. The `Inserter` interface,** with what exists: the input method, wtype,
and the clipboard. No new mechanism, just the shape that lets one be chosen
per insertion.

**3. The RemoteDesktop portal.** This is the Ubuntu phase and the bulk of the
work. One session supplies everything GNOME lacks: `NotifyKeyboardKeysym` for
keystrokes, `ConnectToEIS` for a libei socket instead of a D-Bus round trip
per character, and `clipboard_enabled` to unlock
`org.freedesktop.portal.Clipboard`. `persist_mode` 2 with the returned
`restore_token`, kept in the state directory, means consent is asked once
ever.

Order within it: keystrokes first, since they work into anything including a
terminal and make dictation possible on Ubuntu at all; the clipboard second,
since it is the fast path and needs the keystroke path anyway to send the
chord.

Two things are unsettled, and neither can stop it working. What does GNOME's
consent dialog say, given it is worded for remote control? And does the
restore token survive a reboot as well as a session? Both are presentation and
bookkeeping rather than mechanism.

The two that could have stopped it are answered. Arbitrary text is typeable,
because the session's `SetKeymap` takes an XKB keymap over a file descriptor.
And gnome-shell's privacy indicator does not appear, since it is gated on
`is_recording` and a keyboard-only session is not recording.

**4. uinput,** as the fallback where no portal exists, and the only mechanism
that works on a bare TTY. After the portal rather than before it: uinput needs
a udev rule and group membership, which is a packaging burden per
distribution, and the portal is what avoids it on the desktops that matter.

**5. AT-SPI.** Deferred, and possibly dropped. It cannot reach a terminal,
which is a large share of what gets dictated into, and reaching applications
that hide their accessibility tree may mean announcing diktat as a screen
reader and turning accessibility on globally. Worth an hour with `busctl`
before it is worth a phase.

**6. xdotool, for X11.** Small, and increasingly beside the point: recent
GNOME has removed the X11 session, so on a current Ubuntu there is no X
session to fall back to.

**7. Focused-window identification, best effort.** sway and i3 through their
IPC, Hyprland through `hyprctl`, KDE through D-Bus, GNOME not at all without a
shell extension. Optional, since it only feeds an override — but its absence
on GNOME is what forces the paste chord there to be a setting rather than a
lookup.

**8. A real status channel.** The Pango file is a waybar integration, not an
interface. A unix socket carrying JSON events, with the files kept for the
simple case, is what a tray icon or a window would subscribe to.

**9. Packaging.** A flake reaches nix users. Reaching anyone else means
release binaries, and cgo means each carries libtranscribe. Flatpak needs
deciding separately: the sandbox is hostile to `/dev/uinput` and to spawning
`wl-copy`, though the portal path is exactly what a sandbox is designed for.


## What this does not solve

- **Terminals off wlroots** get keystrokes or a paste, never a whole-string
  insertion. AT-SPI cannot reach a pty and no other mechanism applies.
- **Naming the focused window on GNOME**, without asking the user to install a
  shell extension. That limits `typing_methods` overrides there, not
  insertion.
- **Applications that expose no accessibility.** Toolkits announce themselves
  when they believe an assistive technology is running, and Chromium watches
  for one, so reaching those may mean declaring diktat a screen reader —
  turning accessibility on globally, at a cost to some applications and
  visibly to the user. Whether that trade is acceptable is a phase 3 question
  and may end with AT-SPI being opt-in.
- **Push to talk.** Binding press and release separately is a wlroots and KDE
  ability; GNOME's custom shortcuts fire once. Toggle works everywhere.
- **A software Vulkan device.** On a machine with no real GPU, Mesa's llvmpipe
  may present itself as a Vulkan device, and `placement` in `internal/asr`
  takes the first `DeviceGPU`, which would be slower than the CPU backend it
  displaced. `doctor` flags a software rasteriser by name; whether the library
  reports one as a GPU is still unverified, and the first headless VM in
  `compositor-testing.md` answers it.


## Open questions

1. Does `ext-foreign-toplevel-list-v1`, which is in `wayland-protocols`
   staging and therefore portable, carry enough state to name the *focused*
   toplevel? If so, phase 5 collapses from four integrations into one.
2. How much of the field exposes `EditableText` without a screen reader
   running? This decides whether phase 3 is a fast path or a curiosity.
3. Does any wlroots compositor implement the RemoteDesktop portal? Not needed
   — the input method and wtype are both there — but it would make phase 4 the
   single keystroke path everywhere rather than one of three.
4. Is the clipboard fallback chosen per window by measurement, or per
   application by a remembered verdict? The second is a cache that can be
   wrong for a long time; the first repeats a slow insertion at least once.
