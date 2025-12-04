#!/usr/bin/env python3
"""Whisper daemon - keeps model loaded, toggles recording on signal."""

import signal
import subprocess
import sys
import os
import numpy as np
import sounddevice as sd
from faster_whisper import WhisperModel

SAMPLE_RATE = 16000
MODEL = "deepdml/faster-whisper-large-v3-turbo-ct2"
PID_FILE = "/tmp/whisper-dictation-daemon.pid"
STATUS_FILE = "/tmp/nerd-dictation-status"
STATUS_LOADING = '<span color="#fabd2f">● LOAD</span>'
STATUS_REC = '<span color="#fb4934">● REC</span>'
IDLE_TIMEOUT = 5 * 60  # 5 minutes


def set_status(status: str):
    try:
        with open(STATUS_FILE, "w") as f:
            f.write(status)
    except OSError:
        pass


def main():
    # Write PID file
    with open(PID_FILE, "w") as f:
        f.write(str(os.getpid()))

    set_status(STATUS_LOADING)
    print(f"Loading {MODEL}...", file=sys.stderr)
    model = WhisperModel(MODEL, device="cuda", compute_type="float16")
    print("Model loaded.", file=sys.stderr)

    # State
    recording = False
    audio_chunks = []
    stream = None

    def audio_callback(indata, frames, time_info, status):
        if recording:
            audio_chunks.append(indata[:, 0].copy())

    def start_recording():
        nonlocal recording, audio_chunks, stream
        signal.alarm(0)  # Cancel any pending timeout
        audio_chunks = []
        stream = sd.InputStream(
            samplerate=SAMPLE_RATE,
            channels=1,
            dtype=np.float32,
            callback=audio_callback,
        )
        stream.start()
        recording = True
        set_status(STATUS_REC)
        print("Recording...", file=sys.stderr)

    def stop_recording():
        nonlocal recording, stream
        recording = False
        if stream:
            stream.stop()
            stream.close()
            stream = None
        set_status("")

        if not audio_chunks:
            print("No audio.", file=sys.stderr)
            return

        audio = np.concatenate(audio_chunks)
        print(f"Transcribing {len(audio)/SAMPLE_RATE:.1f}s...", file=sys.stderr)

        segments, _ = model.transcribe(audio, language="en", beam_size=5, vad_filter=True)
        text = "".join(s.text for s in segments).strip()

        if text:
            subprocess.run(["wtype", "--", text], check=False)
            print(f"Typed: {text}", file=sys.stderr)

        signal.alarm(IDLE_TIMEOUT)  # Start idle timeout

    def toggle(sig, frame):
        nonlocal recording
        if recording:
            stop_recording()
        else:
            start_recording()

    def shutdown(sig, frame):
        nonlocal recording
        if recording:
            stop_recording()
        try:
            os.unlink(PID_FILE)
        except OSError:
            pass
        print("Daemon stopped.", file=sys.stderr)
        sys.exit(0)

    def idle_timeout(sig, frame):
        if not recording:
            print("Idle timeout, shutting down.", file=sys.stderr)
            shutdown(sig, frame)
        # If recording, ignore - alarm will be reset when recording stops

    signal.signal(signal.SIGUSR1, toggle)
    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    signal.signal(signal.SIGALRM, idle_timeout)

    # Start recording immediately
    start_recording()

    # Wait forever
    while True:
        signal.pause()


if __name__ == "__main__":
    main()
