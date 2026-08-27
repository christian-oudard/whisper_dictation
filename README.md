# Diktat

State-of-the-art voice typing for your Linux computer.

You are the dictator of your own computer. Press a key, tell your agents what needs to happen next, and it happens.


## Install

In your Nix config:
```
    inputs.diktat.url = "github:christian-oudard/diktat";
    # and in home-manager
    imports = [ inputs.diktat.homeManagerModules.default ];
```

Or on your Nix user profile, :
`$ nix profile add .`

With Go, from a checkout:
`$ go build -o diktat ./cmd/diktat`

(`libtranscribe` has to be installed first)

You can check which version you installed with `diktat version`.


## Download a speech model

The daemon can be started before any of this; with no model it waits and says
so, and loads the first one you name.

First, download a small speech model. The best small model is currently `parakeet-tdt_ctc-110m`.
`$ diktat model parakeet-tdt_ctc-110m`

Or, browse the model menu.
`$ diktat model`

If you are using a GPU, then my pick for best model in 2026 would be `parakeet-tdt-0.6b-v3`.


## Running the Diktat daemon

The next thing to do is start the daemon. The daemon process is responsible for loading models into memory, recording your voice when you press a button, and sending text to your current window.

If you installed with Nix, then the daemon should already be running with systemd, but you can check and start it manually when needed.
`$ systemctl --user enable diktat`
`$ systemctl --user start diktat`
`$ systemctl --user status diktat`

You can also just run the daemon directly in a terminal.
`$ diktat daemon`

You can check logs with `journalctl --user -u diktat`.


## Key bindings

There are two buttons, one to `toggle` and one to `repeat`.

* When you press the `toggle` button, it toggles recording on, then you speak, then when you
press it again the recording stops, and your result is printed.
* When you press the `repeat` button, it re-prints the previous result.


### Sway

Add these lines to your `~/.config/sway/config`:
```
bindsym XF86Assistant exec diktat toggle
bindsym XF86HangupPhone exec diktat repeat
```

*To-Do*: key bindings for other window managers.


## Reporting a bug

Run `diktat doctor` from inside your graphical session and include what it
prints. It reports the compositor, the Wayland protocols that compositor
offers, which helper programs are installed, and what the speech model would
run on. Those decide whether diktat can type at all, and they cannot be
guessed from here.

It reports; it does not change anything.
...
