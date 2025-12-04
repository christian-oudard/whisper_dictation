#!/usr/bin/env python3
"""Toggle whisper recording. Starts daemon if not running."""

import os
import signal
import subprocess
import sys

PID_FILE = "/tmp/whisper-dictation-daemon.pid"


def get_daemon_pid():
    """Get daemon PID if running."""
    try:
        with open(PID_FILE) as f:
            pid = int(f.read().strip())
        # Check if process exists
        os.kill(pid, 0)
        return pid
    except (FileNotFoundError, ValueError, ProcessLookupError):
        return None


def main():
    pid = get_daemon_pid()

    if pid is None:
        # Start daemon (it auto-starts recording after loading)
        subprocess.Popen(
            ["whisper-dictation-daemon"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
    else:
        # Toggle recording
        os.kill(pid, signal.SIGUSR1)


if __name__ == "__main__":
    main()
