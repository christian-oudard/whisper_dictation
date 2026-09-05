# Testing against real compositors: a proposal

The matrix in `linux-support.md` was read off source trees: KWin has
`inputmethod_v1.cpp` and no v2, Mutter has a text input and no input method at
all. That is good evidence about what a compositor implements and no evidence
at all about what happens when diktat talks to it. This is how to turn one
into the other.

Nothing here is built. What *is* built is the layer below it: a fake
compositor in `internal/wayland/compositor_test.go` that decodes requests and
answers them, rather than replaying a script. It covers the success path, the
three reasons to fall back to typing, and the registry listing, in eight
milliseconds and with no VM.

It is not a substitute for any of this. It answers the way the protocol says
to, which makes it a check on our reading of the protocol and no evidence at
all about what a compositor does. It was written after a bug where diktat sent
a well-formed request that asked for the wrong thing, and sway answered
correctly; a fake built from the same misreading would have agreed. That is
exactly the gap a real compositor closes.


## Headless is enough

Every compositor worth testing runs without a display, so none of this needs a
screen, a GPU or a person watching.

- **wlroots** — sway, Hyprland, river, niri, Wayfire: `WLR_BACKENDS=headless`
  with `WLR_LIBINPUT_NO_DEVICES=1`.
- **KWin**: `kwin_wayland --virtual --width 1280 --height 720`. The option is
  `virtualFbOption` in `src/main_wayland.cpp`, "render to a virtual
  framebuffer".
- **Mutter**: `mutter --wayland --headless --virtual-monitor 1280x720`. All
  three are in `src/core/meta-context-main.c`.
- **Weston**: `weston --backend=headless`.
- **X11**: `Xvfb` and any window manager.

Having no GPU is not only tolerable, it is the point of one of these runs. A
headless VM presents either no Vulkan device or llvmpipe, and llvmpipe
reporting itself as a GPU is the unverified risk `linux-support.md` lists:
`placement` in `internal/asr` takes the first `DeviceGPU` and would choose
software rasterisation over the CPU backend it displaced. The first VM that
boots answers that, whatever else it is testing.


## The harness

A NixOS VM test per compositor, declared in the flake and run as
`nix build .#checks.x86_64-linux.<compositor>`.

This fits better than containers or hand-run images for three reasons. The
repo is already a flake, so the compositors are one attribute each rather than
an install script per distribution. The VM boots real systemd and logind,
which is what the compositors expect and what the daemon's own unit needs, so
the session discovery in `env.go` is under test rather than stubbed. And the
driver is a Python script that can assert, so a run either passes or says what
differed.

The cost is a VM boot per compositor: minutes, not the seconds `go test`
takes. These must not join the Go tests. They are a separate target, run when
`internal/output` or `internal/wayland` changes and before a release.


## The part that is actually work: something to type into

An insertion test needs a focused window with a text field and a way to read
back what arrived. Three ways to get one.

**A terminal.** `foot` is native Wayland, supports `text-input-v3`, and can be
launched running `sh -c 'cat > /tmp/received'`, which makes the readback a
file. Cheapest by a wide margin, and it exercises a real client rather than
our idea of one.

**A toolkit entry.** A dozen lines of Python driving a GTK `Entry` that prints
on change. GTK and Qt are what most applications are, so this is the case that
generalises. Worth having as a second client precisely because it is a
different implementation of the same protocol.

**A `text-input-v3` probe we write.** The mirror of the input method client
already in `internal/wayland`: bind `zwp_text_input_manager_v3`, enable, print
the `commit_string` events that arrive. It would isolate the compositor's
behaviour from any toolkit's, which is the one thing the other two cannot do.
But a text input needs a *focused surface*, and getting one means
`wl_compositor`, `xdg_shell`, `wl_shm` and a buffer to map — several hundred
lines past anything the input method client needed, none of it reusable
elsewhere.

Start with the terminal, add the toolkit entry, and write the probe only if a
compositor disagrees with a toolkit and we need to know which is wrong.


## What a run asserts

For each compositor, in order:

1. **`diktat doctor` output, captured as an artifact.** Partly a check and
   partly the point: this is the first time the report is read on a machine
   that is not sway, and whether it is legible there decides whether it does
   its job in a bug report.
2. **The protocol list matches what the source trees predicted.** A
   disagreement here means `linux-support.md` is wrong, which is worth knowing
   before anything is built on it.
3. **`wayland.Ready` agrees with the prediction** — usable on wlroots,
   `ErrNoInputMethod` on KWin and Mutter.
4. **A known string inserted and read back byte for byte**, including
   non-ASCII, since `commit_string` carries UTF-8 and the length on the wire
   counts bytes rather than characters.
5. **The fallback, forced.** Hold the seat's input method with a second client
   first; diktat must then meet `ErrUnavailable` and deliver the text through
   wtype anyway. This is the only way to exercise that path deliberately
   rather than waiting to meet it on a user's machine running fcitx5.
6. **On KWin and Mutter, that falling back is what happens** rather than an
   error reaching the user.

`/dev/uinput` is loadable in a NixOS test VM, so phase 2 of `linux-support.md`
gets a home here too when it exists.


## Order

Chosen by what each one answers rather than by popularity.

1. **sway.** The control. Everything works there today, so a failure is the
   harness rather than the compositor, which is what makes it the right first
   one.
2. **Hyprland.** wlroots, so it should behave exactly as sway does. The first
   actual evidence about portability, and cheap once sway runs.
3. **KWin.** Predicted: no `input-method-v2`, no virtual keyboard, so neither
   of diktat's two mechanisms exists and only the clipboard is left. The most
   informative single VM in the matrix, and the one most likely to embarrass
   the prediction.
4. **Mutter.** Predicted: neither protocol either, and no way to name the
   focused window without a shell extension.
5. **Weston.** The reference implementation, which is how we find out whether
   anything here quietly depends on a wlroots habit.
6. **X11 with i3.** A different axis, and the one where none of the Wayland
   work applies at all.


## What this cannot answer

- **Speed**, which is the entire reason the clipboard path exists. Measuring
  it needs a real application doing real input handling on real hardware, and
  a VM with a software rasteriser and no Electron is not that. The insertion
  tests prove text arrives; they say nothing about how long it took.
- **GPU placement on hardware**, bluetooth SCO, and audio capture. All three
  need the machine they are about.
- **Whether an application supports `text-input-v3` in the field.** A
  compositor relaying a commit and an arbitrary application accepting one are
  different claims, and only the first is testable this way.
