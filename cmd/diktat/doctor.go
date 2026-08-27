package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/christian-oudard/diktat/internal/asr"
	"github.com/christian-oudard/diktat/internal/human"
	"github.com/christian-oudard/diktat/internal/ipc"
	"github.com/christian-oudard/diktat/internal/wayland"
)

// doctor reports the machine rather than diagnosing it. Diktat is meant to
// run on desktops nobody here has, and what decides whether it can type is a
// set of facts the user can see and we cannot: which compositor, which
// protocols it advertises, which helper binaries exist, whether uinput is
// reachable, what the backends found. A bug report that carries this is
// answerable and one that does not is a conversation.
//
// So it states what it found and stops. It offers no fixes, because a fix
// depends on a design that is not built yet, and a wrong suggestion here
// costs more than a missing one.
//
// Deliberately absent from --help, and so from the completions that read the
// command list back out of it. Nobody needs to find this while using diktat;
// they need to be told to run it while reporting something.
func runDoctor(args []string) {
	// Which build this is comes first, because every other line means
	// something different against a different revision, and it is the fact a
	// report is most likely to arrive without.
	fmt.Printf("diktat %s\n", build())

	reportSession()
	reportProtocols()
	reportInputMethod()
	reportHelpers()
	reportUinput()
	reportCompute()
	reportDaemon()
}

// reportSection prints a heading, since this output is read by someone comparing it
// against another machine's.
func reportSection(name string) { fmt.Printf("\n%s\n", name) }

func reportField(name, format string, a ...any) {
	fmt.Printf("  %-20s %s\n", name, fmt.Sprintf(format, a...))
}

// reportEnv reports a variable and whether it is set at all, since an empty value
// and an absent one look the same in a report and mean different things.
func reportEnv(name string) {
	if v, ok := os.LookupEnv(name); ok {
		reportField(name, "%s", v)
		return
	}
	reportField(name, "unset")
}

func reportSession() {
	reportSection("session")
	for _, name := range []string{
		"XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP", "XDG_RUNTIME_DIR",
		"WAYLAND_DISPLAY", "DISPLAY", "SWAYSOCK",
	} {
		reportEnv(name)
	}
}

// waylandName is the Wayland socket to ask about. An inherited WAYLAND_DISPLAY is
// the answer whenever there is one: doctor is run from inside the session,
// which is the case the daemon does not have and the reason its own discovery
// exists.
func waylandName() string {
	return os.Getenv("WAYLAND_DISPLAY")
}

// interesting are the globals that decide how text can reach a window, with
// what each one means, because the interface names are not self-explanatory
// and a report full of them helps nobody.
var interesting = []struct{ name, means string }{
	{"zwp_virtual_keyboard_manager_v1", "wtype works here"},
	{"zwp_input_method_manager_v2", "input method, wlroots flavour"},
	{"zwp_input_method_v1", "input method, KDE flavour"},
	{"zwlr_data_control_manager_v1", "clipboard without focus (wl-copy)"},
	{"ext_data_control_manager_v1", "clipboard without focus (wl-copy)"},
	{"zwlr_foreign_toplevel_manager_v1", "can name the focused window"},
	{"ext_foreign_toplevel_list_v1", "can list windows"},
}

func reportProtocols() {
	reportSection("wayland protocols")
	name := waylandName()
	if name == "" {
		reportField("(skipped)", "WAYLAND_DISPLAY is unset; run this inside the session")
		return
	}
	globals, err := wayland.Globals(name)
	if err != nil {
		reportField("(failed)", "%v", err)
		return
	}
	named := map[string]bool{}
	for _, p := range interesting {
		named[p.name] = true
		state := "no"
		if wayland.Has(globals, p.name) {
			state = "yes"
		}
		fmt.Printf("  %-34s %-4s %s\n", p.name, state, p.means)
	}

	// Then everything else, because the list above is what we know to ask
	// about today and a compositor nobody here has run is exactly the case
	// where the interesting global is one not on it. Sorted, so two machines'
	// reports can be diffed; unannotated, because inventing a gloss for 50
	// interfaces would be worse than the names.
	var rest []string
	for _, g := range globals {
		if !named[g.Interface] {
			rest = append(rest, fmt.Sprintf("%s v%d", g.Interface, g.Version))
		}
	}
	sort.Strings(rest)
	reportSection(fmt.Sprintf("wayland protocols, the other %d", len(rest)))
	for _, name := range rest {
		fmt.Printf("  %s\n", name)
	}
}

// reportInputMethod runs the handshake a dictation would run and says where
// it got to. The protocol being advertised is not the same as it being usable:
// another input method may already hold the seat, and the window in front of
// the user may have nowhere to put text. Both are ordinary, and both are the
// difference between text arriving in one message and being typed a character
// at a time.
func reportInputMethod() {
	reportSection("input method")
	name := waylandName()
	if name == "" {
		reportField("(skipped)", "WAYLAND_DISPLAY is unset")
		return
	}
	switch err := wayland.Ready(name); {
	case err == nil:
		reportField("usable", "yes, the focused window would take a commit")
	case wayland.Unavailable(err):
		reportField("usable", "no: %v", err)
		reportField("so", "text is typed a character at a time instead")
	default:
		reportField("failed", "%v", err)
	}
}

// tools are every binary diktat spawns or might spawn. The ones it does not
// use yet are here because their absence is what a report has to explain when
// a future backend does not appear.
var tools = []struct{ name, used string }{
	{"wtype", "types text on wlroots"},
	{"wl-copy", "clipboard paste"},
	{"wl-paste", "clipboard paste"},
	{"swaymsg", "names the focused window on sway"},
	{"espeak-ng", "the warmup's synthesised speech"},
	{"hyprctl", "names the focused window on Hyprland"},
	{"ydotool", "types text anywhere, through uinput"},
	{"dotool", "types text anywhere, through uinput"},
	{"xdotool", "types text on X11"},
}

func reportHelpers() {
	reportSection("helper binaries")
	for _, t := range tools {
		path, err := exec.LookPath(t.name)
		if err != nil {
			path = "missing"
		}
		fmt.Printf("  %-10s %-4s %-38s %s\n", t.name, verdict(err == nil), t.used, path)
	}
}

func verdict(ok bool) string {
	if ok {
		return "yes"
	}
	return "no"
}

// reportUinput checks the only text-insertion mechanism that works on every desktop,
// including GNOME, and the only one that needs a permission rather than a
// package. Opening it is the whole test: group membership and udev rules are
// how it is granted, not what it is.
func reportUinput() {
	reportSection("uinput")
	const dev = "/dev/uinput"
	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err == nil {
		f.Close()
		reportField(dev, "writable")
		return
	}
	if os.IsNotExist(err) {
		reportField(dev, "absent (the uinput module is not loaded)")
		return
	}
	reportField(dev, "not writable: %v", err)
}

func reportCompute() {
	reportSection("compute")
	reportEnv("DIKTAT_GPU")
	devices, err := asr.Devices()
	if err != nil {
		reportField("(failed)", "%v", err)
		return
	}
	for _, d := range devices {
		name := d.Description
		if name == "" {
			name = d.Name
		}
		note := ""
		if software(name) {
			// A software rasteriser is a Vulkan device that is slower than the
			// CPU backend it would displace, so seeing one chosen is the
			// finding, not the device.
			note = "  [software rasteriser]"
		}
		reportField(fmt.Sprintf("device %d", d.Index), "%-5s %-7s %s (%s free of %s)%s",
			d.Type, d.Kind, name, human.Bytes(d.MemoryFree), human.Bytes(d.MemoryTotal), note)
	}
	chosen, err := asr.Chosen()
	switch {
	case err != nil:
		reportField("would use", "%v", err)
	case chosen == "":
		reportField("would use", "the CPU")
	default:
		reportField("would use", "%s", chosen)
	}
}

// software reports whether a device name is one of the CPU implementations of
// Vulkan. They report themselves as GPUs and are not.
func software(name string) bool {
	name = strings.ToLower(name)
	for _, s := range []string{"llvmpipe", "swiftshader", "lavapipe", "softpipe"} {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}

func reportDaemon() {
	reportSection("daemon")
	pid := ipc.ReadPID()
	if pid == 0 {
		reportField("running", "no")
		return
	}
	reportField("running", "pid %d", pid)
	reportField("binary", "%s", ipc.ExePath(pid))
	reportFile("model", ipc.ModelPath)
	reportFile("activity", ipc.ActivityPath)
	reportFile("status", ipc.StatusPath)
}

// reportFile prints the contents of one IPC file. Absent is a normal state for
// every one of them, and says something different in each case, so it is
// reported rather than skipped.
func reportFile(name string, path func() (string, error)) {
	p, err := path()
	if err != nil {
		reportField(name, "%v", err)
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		reportField(name, "absent")
		return
	}
	reportField(name, "%s", strings.TrimSpace(string(raw)))
}
