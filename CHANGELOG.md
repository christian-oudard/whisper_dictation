# Changelog

## 1.0.0

The first release.

### Dictation

Press a key, speak, press it again, and the words are typed into whatever
window has focus. A daemon holds one speech model for the whole session, so a
dictation costs the time it takes to say it rather than the time it takes to
load a model. Speech recognition runs locally, on a Vulkan GPU where there is
one and the CPU otherwise; no audio leaves the machine and nothing needs a
network once a model has been fetched.

Thirteen models in the menu, none bundled, all fetched on request into
`~/.cache/diktat/models`. The default runs usably on a machine with no GPU.

### Desktops

Wayland and X11. The daemon is a user service and inherits neither session, so
it finds one when it has something to type: the session's own process carries
`WAYLAND_DISPLAY`, or `DISPLAY` and the `XAUTHORITY` that goes with it. Wayland
is looked for first, since a Wayland session runs Xwayland and holds both.

Typing has no portable tool, so three are tried in order and the failure
decides: `wtype` on the wlroots compositors, `ydotool` where the
virtual-keyboard protocol is missing (GNOME, KDE), `xdotool` on X11. The
clipboard that the paste methods borrow follows the session the same way:
wl-clipboard on Wayland, `xclip` or `xsel` on X11. A failure names every tool
tried and what each said.

Per-application paste methods work wherever focus can be asked for: sway,
Hyprland, and any X11 window manager. The other Wayland compositors -- river,
wayfire, labwc, niri -- are found and typed into.

### Knowing what it is doing

A status file for the bar says REC while recording, TX from the press until the
text appears, LOAD while a model is being read, and ERR when a press put no
words on screen. Three things do that: no model, a microphone that delivered
nothing, and text that could not be typed, which `diktat repeat` still has. The
journal says which. A capture that is merely silent is not one of them.

The daemon waits rather than exits when there is no model to load, which is
what a fresh install is: it says what to type and loads whatever `diktat model`
names next. Exiting meant the unit restarted it until the start limit gave up,
after which downloading a model changed nothing.

### Transcribing recordings

`diktat transcribe` turns a recording into a markdown document with speaker
labels. One entry clusters speaker embeddings rather than working to a fixed
cap, so a recording of more than four people is a document about more than
four people; it runs a voice activity detector first, because loudness cannot
tell a voice from a chair. `-s` says how many people are in the room when you
know, which the clustering takes as given rather than estimating -- worth
giving, since the estimate cannot tell one person recorded two ways from two
people. Any format ffmpeg reads goes in. Cuts fall in the quietest moment near
the limit rather than at a fixed offset, speakers are attributed per word
rather than per segment, and paragraphs break where the speaker paused after
finishing a sentence. The turns are saved beside the document, so changing how
it looks costs no second pass over the audio.

### Packaging

Nix (flake and home-manager module), Arch (PKGBUILD), Debian and Ubuntu
(`debian/`), Fedora and openSUSE (RPM spec). All four build the speech library
from source through one shared recipe and link it statically, so what is
installed is a single binary. There is a systemd user unit, a zsh completion
and a manual page.

### Known limitations

**Speaker counts run one over.** Measured against four AMI meetings with four
speakers each, three come back with five. The extra cluster is audible
non-speech -- a chair, a keyboard -- that the energy gate passes and the
clustering then groups as a thing that is none of the voices. Pass `-diarizer`
with a speaker count where you know it.

The fix is a voice activity detector, which is a model rather than a threshold:
seven repairs that were not models were measured and none worked. One is now
ported in transcribe.cpp and not yet reachable from here. On those same four
meetings its regions improve speaker confusion on every one and fix the count
on two of the three that were over; the third keeps its extra cluster, and a
fourth meeting returns three speakers because one of its four talks for 19.5
seconds in 13.6 minutes and has never been resolved here, with the detector or
without it. The numbers are in transcribe.cpp's `docs/diarization.md`.

**Overlapping speech is attributed to one speaker.** In published diarization
numbers overlap costs about twenty points against one or two for the choice of
clustering, so this is the largest thing not done. It needs source separation
rather than better clustering.

**Two live compositors is an error rather than a choice.** Either one is a
plausible target and the wrong one puts a dictation on the wrong screen.

**The model menu is not versioned with this release.** Models are fetched from
upstream repositories at run time, and an entry can stop resolving without
anything here changing.
