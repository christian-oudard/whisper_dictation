# What is not done

An index, because the answer was spread across five files and some of it was
in nobody's file at all. Anything with a home is a pointer rather than a copy:
a second statement of the same thing is what drifts.


## Recorded elsewhere

- **Known limitations of the release** -- overlapping speech attributed to one
  speaker, which is the largest thing not done and needs source separation
  rather than better clustering; the speaker count on sparse speakers; two
  live compositors being an error; the model menu not being versioned with the
  release. `CHANGELOG.md`.
- **Desktops other than sway.** What ports, what is bound to sway, and the
  phases to get off it. `docs/linux-support.md`, whose Open questions section
  is the live part.
- **Testing against real compositors.** Designed, not built. The in-process
  fake covers our reading of the protocol and nothing about what a compositor
  does. `docs/compositor-testing.md`.
- **Streaming transcription.** Decided against for now, with the reasons, so
  the decision is made once. `docs/streaming.md`.
- **Key bindings for other window managers.** `README.md`.


## With no other home

- **The `tx-model` menu is hard to choose from, and its default is a trap.**
  The default entry, `multitalker-parakeet`, stops at four speakers and
  refuses `-s` outright, so the flag the README teaches fails until the user
  picks entry 1. Six entries are named by architecture rather than by what
  they are for, and the notes cite `cpWER` and "whole-file speaker track" at
  someone choosing their first one. Two entries exist mostly to be warned
  against. Open: whether to cut the menu, change the default to the entry that
  counts speakers, or only rewrite the notes.

- **`-pause` defaults to one second on judgement, not evidence.** The value is
  inside the range of an ordinary pause between utterances, which is the
  property that matters, but no measurement chose it over any other value in
  that range, and none chose it over leaving the audio alone.

- **Words at a chunk seam are not stitched.** Pieces are butt-joined with no
  overlap. Normalizing every piece against the whole recording fixed the
  diffuse part of the divergence; what is left is concentrated at the joins.
  `transcript.Dedupe` already implements the rule this needs -- same word,
  same moment, different stream -- and the piece index could serve as the
  stream.

- **The daemon does no voice activity detection.** Whether running it on live
  dictation would pay is unmeasured. `internal/silence` is about recordings
  and says what the detector costs there.

- **The Wayland constants are still transcribed by hand.** One of them was
  wrong for the life of the input method. `docs/wayland.md` has the case for
  generating them from XML and what it would take, including that the XML is
  in no package and would have to be vendored.
