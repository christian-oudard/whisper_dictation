# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Whisper-based voice dictation for Linux/Sway/Wayland. Uses faster-whisper with GPU acceleration for high-quality offline speech-to-text.

## Repository Structure

- `whisper_dictation/` - Python package
  - `daemon.py` - Daemon that keeps Whisper model loaded in VRAM
  - `toggle.py` - Toggle command that starts daemon or toggles recording
- `pyproject.toml` - Package definition with entry points
- `toggle-nerd-dictation.bash` - Legacy nerd-dictation toggle script

## Key Commands

Install:
```bash
uv tool install .
```

Toggle recording (bind to key):
```bash
whisper-dictation-toggle
```

Stop daemon:
```bash
pkill -f whisper-dictation-daemon
```

## Architecture

**Daemon pattern** for instant recording:

1. `whisper-dictation-toggle` starts `whisper-dictation-daemon` if not running
2. Daemon loads model (shows yellow "LOAD" status)
3. Daemon auto-starts recording (shows red "REC" status)
4. `whisper-dictation-toggle` sends SIGUSR1 to toggle recording on/off
5. On stop: daemon transcribes audio, types result via wtype
6. Daemon stays running with model in VRAM for instant subsequent recordings
7. Daemon auto-shuts down after 5 minutes idle

**Files:**
- `/tmp/whisper-dictation-daemon.pid` - Daemon PID
- `/tmp/nerd-dictation-status` - Status bar output (Pango markup)

## Dependencies

- `faster-whisper` - Whisper implementation with CTranslate2
- `sounddevice` - Audio recording
- `wtype` - Wayland input simulation
- NVIDIA GPU with CUDA for acceleration
