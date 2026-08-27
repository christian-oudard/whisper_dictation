# CLAUDE.md

## Overview

Voice dictation for Linux and Wayland. A daemon holds one speech model for the
whole session, records while a key is held down and typed out, transcribes, and
types the result. Go, built with nix `buildGoModule`.

Speech recognition is transcribe.cpp throughout, linked in through its Go
bindings; there is no second engine and no backend interface. Every model is a
GGUF and the library reads the architecture out of the file, so whisper,
moonshine and parakeet are not distinguishable here.

That engine is kept for breadth rather than for any one model. sherpa-onnx runs
the parakeets well but reaches a GPU only through CUDA, which means an unfree
closure, and drops every audio-LLM; parakeet.cpp is ggml and Vulkan like this
one and adds streaming, but does parakeet and nothing else. The field turns
over every few months, so an engine that takes a new family as another GGUF is
worth more than one that is fastest at the current favourite.

Models are downloaded on demand into the user's cache, never at build time and
never implicitly.

## Layout

- `cmd/diktat/` - the shipped binary, one file per subcommand: daemon, toggle,
  repeat, model, version. `main.go` holds the dispatch table. The zsh
  completion in `completions/` reads the command list back out of `--help`
  rather than keeping a copy, since the copy drifted. The nix build stamps the
  revision and commit date in through ldflags; the date uses a `T` rather than
  a space, since ldflags are joined on spaces.
- `cmd/diktat/daemon.go` - the whole daemon. A model that is missing or will
  not load does not stop it: nothing is bundled and nothing is fetched
  implicitly, so a fresh install has no model, and exiting meant the unit
  restarted every two seconds until the start limit gave up -- after which
  downloading one changed nothing, because what would have loaded it was dead.
  It waits instead, says so on the bar, and loads what `diktat model` names.
  Keeps the model loaded, rehearses
  it between dictations, toggles recording on SIGUSR1, transcribes, types via
  wtype. Runs for the session: it never starts recording by itself and never
  exits by itself. Signal handlers are installed before the model load, so a
  toggle during startup is queued rather than killing the process. Recording is
  unbounded, since how long an utterance may usefully be is the model's answer
  and the daemon asks it.
- `cmd/diktat/model.go` - lists the menu, or switches to an entry by number,
  name or path via SIGHUP, fetching it first if the cache lacks it. There is no
  separate download verb: naming a model is the only reason to want one, and
  the prompt keeps the fetch from being silent.
- `cmd/transcribe/` - runs the daemon's pipeline over WAV files, to compare
  models on fixed audio. Warms first, like the daemon, since an unwarmed model
  cuts long audio differently and that changes the transcript.
- `cmd/warmbench/` - measures what each rehearsal length compiles, which is how
  the bucket set is chosen rather than guessed.
- `internal/models` - the menu: thirteen entries, none bundled, all fetched
  from the `handy-computer` GGUF repos into `~/.cache/diktat/models`, so no
  model is a special case. Ordered by download size, which is roughly the cost
  order, and the number in that listing is how models get selected. The
  language set per model is hand-kept, because the menu has to answer before a
  model is downloaded; a test checks it against the library for whatever is
  present. Several entries are recent enough to have no published accuracy
  figure and are there to be tried, not because they are known good.
- `internal/asr` - one `Model` over transcribe.cpp: load, transcribe, and what
  it costs. Picks the discrete GPU when there is one.
- `internal/audio` - capture through malgo, plus the length buckets and the
  chunking rule. A sample count bounds the buffer against a device that lies
  about its frame rate, which an ALSA null device once did to the tune of
  19279s of audio in 4s of wall clock.
- `internal/warmup` - the rehearsal: the synthesised speech, which lengths to
  rehearse at, and one length at a time. Shared with the offline tools because
  warming is not only about latency; it is also what tells a model how much
  audio it can take in one graph.
- `internal/suspend` - how long this machine has spent suspended, read from the
  two monotonic clocks.
- `internal/wav` - WAV read and write, split out of `internal/audio` so the
  offline tools can read a clip without pulling in malgo.
- `internal/xdg` - the two base directories, so `config` and `ipc` do not each
  have their own idea of where state goes. Deliberately a leaf: `ipc` asking
  `config` for a path put the model downloader, and so the http stack, behind
  every build of a package that names four files.
- `internal/` also holds config, human, ipc and output. `human` renders sizes
  in binary units under one precision rule, since the menu, the download
  progress and the daemon's memory lines print the same kind of number and were
  each rounding it differently.

## Runtime contract

libtranscribe is linked at build time with its backends compiled in, so the
wrapper sets no library-path variables beyond the audio and Vulkan loaders.

External CLIs expected on PATH: `wtype`, `wl-copy`, `wl-paste`, `swaymsg`,
`espeak-ng`. The last is the warmup's synthesised speech.

Text reaches a window by one of three mechanisms, tried in that order.
`internal/wayland` speaks the wire protocol directly and asks the compositor's
input method to commit the whole string in one message, which is the only one
of the three that is not pretending to be a keyboard: it goes in through the
application's own text input path, so undo sees text rather than typing. That
protocol is `zwp_input_method_manager_v2`, a wlroots one, so KWin and GNOME
have nothing to answer with; another input method may hold the seat, since
there may be only one per seat; and the focused window may have no text input
at all. Each is an ordinary state of a working machine, so `Insert` returns a
sentinel and the caller types instead. The connection is opened per insertion
rather than held, because holding it would lock out fcitx5 and ibus for the
session to save under a millisecond.

`diktat doctor` reports which of the three this machine gets, and runs the
input method handshake rather than inferring it from the advertised protocol.
It is hidden from `--help`, and so from the completions that read the command
list out of it; the README names it under bug reporting.

The Wayland ones need `WAYLAND_DISPLAY` and `SWAYSOCK`. The daemon is a systemd
user service wanted by `graphical-session.target`, and it inherits neither, so
`internal/output/env.go` finds them in `XDG_RUNTIME_DIR` when it spawns a
child: the live `sway-ipc.<uid>.<pid>.sock` gives SWAYSOCK, and
`WAYLAND_DISPLAY` is read out of that compositor's `/proc/<pid>/environ` rather
than guessed from the `wayland-N` names beside it, which a greeter or a nested
session also leaves behind. An inherited value always wins, which covers
anything a keybinding runs.

The documented alternative, `systemctl --user import-environment`, has two
costs this does not: it makes the compositor responsible for starting the
daemon, and it copies the values once, so a compositor restart leaves the
daemon typing at a socket nobody holds, since SWAYSOCK names sway's PID.

Two live compositors for one user is an error rather than a pick. Either socket
is plausible and the wrong one puts a dictation on the wrong screen.

## GPU

Built with ggml's Vulkan backend, not CUDA: Vulkan is in the binary cache,
needs no unfree toolchain, and covers Intel and AMD as well as NVIDIA.

The library takes the first device that is a GPU *or* an integrated GPU, so on
a hybrid laptop it lands on the Intel chip instead of the discrete card. An
iGPU shares memory bandwidth with the CPU it would be replacing and is no clear
win, so `placement` in `internal/asr` walks the devices for a discrete GPU,
skips integrated ones, and pins `LoadOptions.GPUDevice`. No discrete device
means CPU. `DIKTAT_GPU=0` forces CPU, `=1` takes whatever the library would
have picked unaided.

## Warmup

Loading a model is not the same as being ready to use it. The Vulkan backend
compiles its shaders on the first graph run and ggml allocates its compute
buffers there too, both per graph shape, and the shape follows the length of
the audio. So this is not a cost paid once.

The daemon rehearses on throwaway speech at each length bucket in
`internal/audio`: 1, 2, 3, 5, 7, 10, 15, 20, 25 and 30 seconds. The backend
picks its matmul variants in bands rather than per sample, so rehearsing inside
a band warms the whole band, and the buckets only have to be dense enough to
enter every band once.

Rehearsal runs between dictations, not before the first one: a model can
transcribe as soon as it is loaded, so the daemon installs it and then works
through the buckets in the gaps, only while nothing is recording and no other
model is loading. Starting a recording cancels the bucket in flight. A
cancelled length keeps its place, since the run may have stopped before it
compiled anything. Progress is per model, so a model rehearsed earlier resumes
where it left off.

None of this is on the status bar. LOAD means a model is being read and is
about whether dictating is possible right now; a rehearsal in the background is
not, so it lives in the activity file where `diktat model` reads it.

Three things not to redo, each tried and reverted:

- Sparse buckets. Rehearsing at 1 and 30 seconds left moonshine compiling at 20
  and granite at 2, 5 and 10. ggml-vulkan picks variants from the matrix
  dimensions and the device's core count, so which shapes are distinct is a
  property of the model and card together, not of the architecture.
- Rounding every utterance up to a bucket. The encoder work it adds is charged
  forever where the compiles it avoids are paid once. Only very short audio is
  still lifted, to the smallest bucket, because below that the shape is
  genuinely unrehearsed.
- Cutting long audio into bucket-sized pieces. Models window long audio
  themselves and do it better; cutting mid-sentence was audible in the
  transcript on every family. `audio.Chunk` now cuts only what a model would
  refuse or the card could not hold.

### Waking the card

A card left alone drops its clocks, and the next work pays to bring them back.
A dictation absorbs this: the daemon starts a one second rehearsal when
recording starts, and it hides behind the speech.

A load pays it far worse, because a load is thousands of small transfers and
none is long enough to clock the card up by itself. Measured on a laptop RTX
4070, the same model loaded in 183ms straight after a rehearsal and 1m50.8s
after thirty seconds of quiet. So `startLoad` runs `wakeRun` first and the load
waits for it. Nothing to wake with is fine: a bucket in flight means the card
is already busy, and no model at all means this is the session's first load.

Nothing here sleeps. The rehearsal is a graph run and the load waits for it to
finish, not for an interval. A second of audio is not a second of wall clock:
it costs about 130ms on a card at its clocks and about 880ms on one that is
not, which is the point, since what is paid is whatever that card needed.

The clip is one second because that is the shortest bucket. Shorter clips wake
the card just as well and cost the same, but are shapes no bucket rehearsed, so
the first one compiles.

Do not replace the graph with something cheaper. Two candidates were measured
and neither works: `transcribe.Devices()` resumes the device from D3cold and
the load after it is still slow, which is why `asr.Load`'s own device query
never helped, and the PCI core's `power_state` is exact, free and answers the
wrong question, since the card sits in D0 at idle clocks. Clocks come up only
under load and nothing portable reports them, so running the graph is what
raises them and its duration is the only reading available. It is logged as
`woke`.

### Losing a model to a suspend

Unless the driver was told to preserve video memory across a sleep, the weights
are discarded, and a model without them does not fail: it runs its graphs at
the usual speed and returns nothing, on every utterance, until something
reloads it.

The daemon notices the sleep rather than inferring it from the silence.
CLOCK_BOOTTIME is CLOCK_MONOTONIC plus time spent suspended, so their
difference is a ledger of sleep that nothing else moves, NTP included, and
`internal/suspend` reads it on a two second ticker. When it grows the model is
reloaded off the main loop. Every load carries the suspend count it started
under, so one that was reading the card when the machine slept is closed
unopened and run again. This needs nothing from logind or D-Bus and counts
sleeps nothing orchestrated. A model on the CPU is not reloaded, since RAM is
what a suspend preserves.

The wake run is the backstop, since it is the only audio all session whose
words are known before it is transcribed. It catches a dictation begun inside
the reload window, and video memory lost to anything that is not a sleep.
Silence where there were words before means the model is gone, and the daemon
reloads before transcribing the capture it is holding, so that dictation is not
lost. Only a model that has answered the clip before is judged, or one with no
words for it would be reloaded before every dictation; that baseline comes from
the rehearsal, which runs the same speech within a second or two of every load.

A reload replaces the model but not the device it lives on, so it is checked
against the same clip, and one that is still mute exits for the unit's
`Restart` to turn into a process restart.

## Audio length

Every family except whisper encodes the whole clip in one graph, and the
activations grow with its length, so there is a length at which the card cannot
hold the graph. The Vulkan backend does not survive that: the allocation fails
and the process dies, which for a daemon means dictation stops mid-sentence.

`MaxAudio`, the model's own declared ceiling, does not predict this and cannot.
On an 8 GB laptop card, granite advertises 6m24s and dies at 3 minutes;
canary-180m-flash advertises no limit and dies at about 5.

So the limit is measured. ggml keeps compute buffers at the high-water mark, so
a clip no longer than one already run allocates nothing and is always safe;
`Model.Transcribe` reads the device across the clips that are longer, which are
the only ones that allocate. `Model.AudioLimit` is the longest clip already run
plus what a quarter of the device's free memory buys at that measured rate,
capped by `MaxAudio` where one is declared. A quarter because this extrapolates
past every length measured and the slope taken from short clips understates the
true one; being wrong the safe way costs a seam, and the other way costs the
daemon. The estimate re-anchors on every clip, so the limit climbs with use.

A model that has run nothing has no rate and gets 30 seconds: what the warmup
rehearses to and what whisper windows to internally. Where the cut falls
changes what comes back, which is why `internal/warmup` is shared rather than
living in the daemon: a benchmark that skips warming is not measuring the
daemon's pipeline.

## The microphone going away

The capture device is opened once and never closed; `Recorder.Start` and
`Stop` only gate whether the callback keeps anything. That holds a bluetooth
headset in its headset profile for the whole session, which is the right trade
for a tool that is always about to want the microphone, and it is also what
leaves the headset's SCO link up until something breaks it. A broken link is
not an error: the source stays RUNNING and unmuted and delivers bit-exact zero
for the rest of the session, so every dictation types nothing.

`Recorder.Rebuild` closes and reopens the device, which renegotiates the
profile on its own, so nothing here knows what bluetooth is. When to do it is
the hard half, and `internal/sco` is the answer: it asks the kernel for the
adapters' connection list, where a live headset microphone is one synchronous
link and a dead one is none. The daemon watches for that link disappearing on
its existing ticker and rebuilds between dictations, so the loss costs nothing.
`audio.IsDead` reads a whole capture for the same failure and is the backstop
for a microphone that dies some other way.

Three earlier signals are ruled out by measurement, and the comments in those
files carry the numbers: the audio itself, because a headset gates its own
silence to bit-exact zero for seconds at a time; the source's `bluez5.profile`
property, which reads `off` on a healthy link and a dead one; and the kernel's
`SCO packet for unknown connection handle` line, which ordinary teardown emits
too, `Rebuild` included.

## Speaker labels

Dictation never asks who is speaking: one person holds the microphone. A
recording of a talk or an interview is the other shape, so `diktat tx-model`
is a second menu over `internal/models.Diarizers`, `diktat transcribe` runs an
entry over a file, and `cmd/diarize` runs a diarizer alone. Input is whatever
the recorder wrote: anything that is not already a 16 kHz WAV goes through
ffmpeg. The document is what was said and nothing else, with the run's own
figures logged rather than printed into it; `-timestamps` adds the time of each
turn, for a transcript that is an index into the audio. Paragraphs are not
invented: a break is a two second pause after a finished sentence, which the
speaker put there and the word stamps report. `-lang list` names the codes each
entry takes, since the menu column names the reach of a set of 25 and the
setting is given a member of it.

It shares no entries with the first menu, on a capability rather than a
preference. Attributing text to a speaker is a join between words and speaker
rows, so a model that cannot stamp below a whole segment cannot carry a label
at all. `cmd/caps` reads that out of a GGUF: of the dictation menu only the
parakeets stamp per token, whisper stops at segments, and the rest produce no
timestamps.

The clustering entry is the one that counts rather than assuming, so it is the
only one that can answer a room of more than four. It runs a voice activity
detector first, because loudness cannot tell a voice from a chair and a chair
clusters as a thing that is none of the voices; regions shorter than half a
second are dropped, since nobody says a word in under that and what does fire
there is a cough or a door. `-s` gives the count when somebody knows it, which
is worth doing: the estimate cannot tell one person recorded two ways from two
people, and on three voices it has split one of them in two.

A menu entry is a whole pipeline rather than a model, because the composition
is not a choice worth offering: sortformer produces no text and must be
paired, and the models that transcribe and attribute in one pass cannot be
paired with anything. Three architectures: a diarizer paired with a model that
supplies the words, `multitalker-parakeet` whose `bundle/` GGUF has sortformer
embedded, and MOSS, an audio-LLM emitting `[Sxx]` tags for the runtime to
parse.

Where the identity comes from is the whole difference between the pairs and
the rest, and it is why only a pair may be cut. A model that attributes its own
speakers numbers them from one in every piece and remembers nothing of the
piece before, so a numbering cannot cross a cut: two pieces both open on
speaker 1 and nothing in the text says whether that is one person or two. That
was reconciled by repeating thirty seconds and matching the numbering over what
both pieces heard, which works only for people who spoke on both sides of the
cut and gave 71 speakers for 12 people. The matcher is gone; such an entry now
refuses a recording it cannot hold in one pass and names one that can.

A diarizer takes the whole recording in one pass and numbers it once, so only
the words are cut and the pair joins the two in `transcript.Attribute`: each
word to the speaker whose rows cover most of it. Per word rather than per segment because a segment ends where the
transcriber drew a sentence and a row ends where somebody stopped talking, so a
sentence across an interruption belongs to two people. A change of speaker
shorter than a second, bracketed by one person, is the two views disagreeing at
an edge rather than an interjection, and is dropped.

Punctuation is asked for rather than assumed: it is off by the multitalker's
family default, so the first full run came back as 109 minutes of unbroken
lowercase. Word timestamps are asked for only where `MaxTimestamps` says the
model has them, since asking a model that has not got them fails the run rather
than being ignored, and a diarizer is asked for nothing finer than Auto.

The speaker cap is the column that decides most choices. Everything descended
from sortformer stops at four, and the only entry with no declared cap is the
audio-LLM, whose working set grows with the recording. Nothing declared is not
the same as unlimited, hence "unstated".

Memory inverts what the dictation menu learned. Sortformer streams with a
speaker cache, so it took a 1h49m recording in one pass at 248 MiB, where
parakeet-tdt-0.6b-v2, which declares no length limit, is measured good for
3m56s on an 8 GB card. Quantization inverts too: nothing here shares a card
with a desktop, so these take near-reference tiers, and sortformer has no
choice, since k-quant error can permute its speaker labels mid-stream.

## One model resident

The model in use is the only one held, and the one it replaces is closed as
soon as the new one can transcribe. Nothing is cached against a switch back.
It was an LRU cache once; canary-qwen-2.5b holds 3.4 GiB of a laptop card's 8
GB, and holding that for a model nobody is using is worse than reloading it.

The load runs off the main loop, so a keypress is still answered while it
happens and the old model keeps serving until the new one is ready. Only one
load runs at a time: a second ask cancels the first rather than queueing behind
it. `asr.Load` cannot be interrupted, so a cancellation lands when it returns
and the model it produced is closed unopened.

What a model costs is measured rather than guessed, since the audio limit is
struck against it: `asr.Load` reads the device's free memory either side of the
load, and the compute buffers are added as transcriptions grow them. A backend
reporting no memory falls back to the file size.

How long it took is split the same way. `asr.Load` reads the file through once
and discards it before handing the path to the library, which reads it again
from the page cache that read just filled. That separates waiting for a disk
from everything the library does afterwards, which is the only seam visible
from here, since `transcribe.Open` is one call that logs nothing timed.

## IPC files

Split by lifetime. In `$XDG_RUNTIME_DIR/diktat/`, which is per-user, mode 0700
and emptied by logind at logout, are the files describing the session:

- `daemon.pid` - so `toggle` knows where to send its signal
- `model` - model directory loaded; a request on the way in, a statement of
  fact on the way out
- `last` - last transcribed text, which is what makes `repeat` possible
- `activity` - `loading <dir>` or `warming <dir>`, absent otherwise. `model`
  cannot answer this: it names the model in use, and the whole question during
  a switch is what is happening to the one that is not in use yet

An unset `XDG_RUNTIME_DIR` is an error rather than a fallback to `/tmp`: the
only fallback available is the place these exist to stay out of, and a Wayland
session always sets it.

In `$XDG_STATE_HOME/diktat/`, beside the remembered model choice, is the one
file another program reads:

- `status` - Pango markup for the bar: REC, TX, LOAD while a model is being
  read, and ERR when the last dictation was transcribed and could not be typed

Both toggles write it before anything else the press sets off, and TX covers
everything between the press and the text. What the daemon happens to be doing
when the key arrives is not the light's business, and REC through any of that
says the mic is live when it is not.

ERR is the one that is about the last press rather than this one: it says the
press put no words on screen. Three things do that -- no model to transcribe
with, a microphone that delivered nothing, and text that could not be typed --
and each is a line in the journal saying what to do. Without it the key looks
like it does nothing, when in fact a desktop with no typing tool fails every
dictation and the text is one `diktat repeat` away, since `last` is written
before the typing is attempted. It outranks idle and nothing else, and the
next press clears it.

A capture that is merely silent is not one of the three. Pressing the key
around silence is an ordinary thing to do and a light that scolds anybody for
it is noise.

It lives with the state rather than the runtime files because a bar's config
has to name the path and `/run/user/<uid>` cannot be written as `~`. The cost
is that a daemon killed outright leaves its last status on screen; the next
start overwrites it.

These were all `/tmp/diktat-*` under fixed names once, which meant two users on
one machine could not both run a daemon, and mode 0644 published what diktat
was doing to everything else running.

## Logging

Not among the IPC files at all: the log goes to stderr, which systemd connects
to the journal itself. It records lengths and timings, never the text.

Two levels, split by what a line answers. What happened is on by default: the
model in use, switches, suspends, anything that failed. How it happened is
behind `DIKTAT_DEBUG`, spelled like `DIKTAT_GPU` because that is the only other
knob here: audio levels, per-stage transcription timings, the warmup's
per-length breakdown, and a load's split into waking, reading and opening. The
split is by frequency as much as by kind, since dictating happens several times
a minute and buried everything else. A load keeps its total at the default
level for the inverse reason: it is rare, the user asked for it, and a slow one
has to be visible without anyone knowing to turn anything on.

The journal stamps every line it captures, so the daemon stamps its own only
when nothing else will, which `JOURNAL_STREAM` answers.

## Build

```
nix build
./result/bin/diktat model parakeet-tdt_ctc-110m
./result/bin/diktat daemon
```

`nix build` only writes ./result and puts nothing on PATH; `nix profile add .`
does that.

The Go bindings to transcribe.cpp live in that project's tree and are vendored
here, so a clean clone builds. Re-running `go mod vendor` needs that checkout
beside this one, since go.mod resolves the bindings through a relative
`replace`.

## Config

`~/.config/diktat/config.toml`, optional:

- `model` - what the daemon starts on before anything has been chosen, default
  `parakeet-tdt_ctc-110m`. `diktat model` records its choice in
  `$XDG_STATE_HOME/diktat/model` instead of writing here, since this file is
  hand-authored; that choice outranks this key, and deleting it restores this
  one.
- `typing_methods` - map of sway app_id to how text reaches it. Every value is
  a paste key combo (`C-v`, `C-S-v`), since pasting is the only alternative to
  wtype so far; the key is named for the choice rather than for today's answers
  to it. Only consulted where the input method declined, since an entry
  describes a slow keystroke path the input method does not use.
- `history_file` - JSONL append target for each transcription. A path, `true`
  for `$XDG_STATE_HOME/diktat/history.jsonl`, or `false` for none, which is
  also what leaving the key out does. It holds what was said, so the file is
  0600.

Unknown keys are reported rather than ignored, since TOML drops them silently
and a key that had quietly stopped meaning anything sat in a real config
looking like it worked. A key with the right name and the wrong type fails the
whole decode, and every caller used to answer that by carrying on with an empty
Config, so a bad `history_file` silently disabled the `typing_methods` below it.
The daemon now stops on a config it cannot parse, with exit 78 (EX_CONFIG) so
the unit does not restart it into the same failure.
