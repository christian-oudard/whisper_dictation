// Daemon: keeps the model loaded, toggles recording on SIGUSR1, transcribes
// on stop, types the result.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/christian-oudard/diktat/internal/asr"
	"github.com/christian-oudard/diktat/internal/audio"
	"github.com/christian-oudard/diktat/internal/config"
	"github.com/christian-oudard/diktat/internal/human"
	"github.com/christian-oudard/diktat/internal/ipc"
	"github.com/christian-oudard/diktat/internal/models"
	"github.com/christian-oudard/diktat/internal/output"
	"github.com/christian-oudard/diktat/internal/sco"
	"github.com/christian-oudard/diktat/internal/suspend"
	"github.com/christian-oudard/diktat/internal/warmup"
)

const (
	statusLoad = `<span color="#fabd2f">● LOAD</span>`
	statusRec  = `<span color="#fb4934">● REC</span>`
	statusTx   = `<span color="#458588">● TX</span>`
	// A press of the dictation key that did not put words on screen: no model
	// to transcribe with, or text that could not be typed. It is on the bar
	// because the alternative is a key that appears to do nothing, when in
	// fact there is a line in the journal saying exactly what to do about it.
	// It stays until the next press, since it describes the last one.
	statusErr = `<span color="#fe8019">● ERR</span>`
)

// exitConfig is EX_CONFIG from sysexits.h, for a config only a person can
// fix. The unit gives it to RestartPreventExitStatus, so the daemon stops
// once and says why, rather than restarting every two seconds until the start
// limit stops it anyway and the reason is twenty entries up the journal.
const exitConfig = 78

func runDaemon(args []string) {
	if len(args) > 0 {
		log.Fatalf("daemon takes no arguments; set model in %s", config.DefaultPath())
	}

	// Install handlers before loading the model: until this runs, SIGUSR1
	// keeps its default disposition and would kill the daemon. A toggle
	// pressed during startup queues here instead.
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGUSR1, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)

	// A config that does not parse stops the daemon. Carrying on with an
	// empty Config was worse than it sounds: the settings that go missing are
	// the ones someone bothered to write, and nothing about dictation looks
	// broken afterwards, it just quietly does the default thing.
	cfg, unknown, err := config.Load(config.DefaultPath())
	if err != nil {
		log.Printf("config: %v", err)
		os.Exit(exitConfig)
	}
	for _, key := range unknown {
		// Silence here is how a key that had stopped meaning anything sat
		// in a real config looking like it worked.
		log.Printf("config: ignoring unknown key %q", key)
	}
	// Logging is stderr and nothing else: under systemd that is the journal,
	// which timestamps every line itself, keeps them across restarts and can
	// be followed while the daemon is running. A file of our own was one more
	// thing to find, and it was truncated at every start, so the log of the
	// crash was gone by the time anyone looked.
	log.SetFlags(logFlags())
	log.Printf("Starting %s %s", gitRev, exePath())

	// Resolved once, here, because a daemon that cannot name these files
	// cannot do its job either, and every use below would otherwise repeat
	// the same fatal.
	pidPath, err := ipc.PIDPath()
	if err != nil {
		log.Fatal(err)
	}
	statusPath, err = ipc.StatusPath()
	if err != nil {
		log.Fatal(err)
	}
	modelPath, err = ipc.ModelPath()
	if err != nil {
		log.Fatal(err)
	}
	activityPath, err = ipc.ActivityPath()
	if err != nil {
		log.Fatal(err)
	}

	if err := ipc.Write(pidPath, []byte(fmt.Sprint(os.Getpid())), 0644); err != nil {
		log.Fatalf("write pid: %v", err)
	}
	defer os.Remove(pidPath)
	defer os.Remove(statusPath)
	setStatus(statusLoad)

	// What to start on comes from config.StartModel, never from the model file
	// above: that file says what a daemon has loaded, and a daemon starting
	// has loaded nothing.
	//
	// Neither a model that is not there nor one that will not load stops the
	// daemon. Nothing is bundled and nothing is downloaded implicitly, so a
	// fresh install has no model at all, and exiting meant the unit restarted
	// every two seconds until the start limit gave up on it -- after which
	// downloading a model changed nothing, because the daemon that would have
	// loaded it was dead. Waiting costs a process that cannot dictate yet and
	// says so; `diktat model` is heard either way, and loads what it names.
	name := config.StartModel()
	modelDir := models.Resolve(name)
	var model *asr.Model
	if err := models.Check(modelDir); err != nil {
		log.Printf("%s is not downloaded, so there is nothing to dictate with yet. Get it with:\n  diktat model %s",
			name, name)
	} else if loaded, err := asr.Load(modelDir); err != nil {
		// The remembered choice is not undone by a load that fails, so every
		// start will fail the same way until someone changes it or replaces
		// the file. Say how to do both.
		log.Printf("load model: %v\nPick another with `diktat model`, or refetch this one:\n  rm -r %s && diktat model %s",
			err, modelDir, name)
	} else {
		model = loaded
	}
	defer os.Remove(modelPath)

	rec, err := audio.NewRecorder()
	if err != nil {
		log.Fatalf("audio recorder: %v", err)
	}
	defer rec.Close()

	d := &daemon{
		recorder:  rec,
		cfg:       cfg,
		loaded:    make(chan loadResult, 1),
		rehearsed: make(chan bucketResult, 1),
		asleep:    suspend.Total(),
		linkWatch: true,
		// A daemon with no model cannot dictate, and a bar showing nothing
		// says the opposite. The journal above says which of the two reasons
		// it is.
		failed: model == nil,
	}
	defer d.closeModel()
	defer os.Remove(activityPath)
	if model == nil {
		d.restoreStatus()
	} else {
		// Ready here, not after the rehearsal: the model can transcribe as soon
		// as it is loaded, and warming it is worth several seconds on a large
		// one. Those seconds are spent between dictations from now on.
		log.Printf("Model loaded: %s", model.Arch())
		debugf("Load: %s", model.LoadTimings())
		d.install(modelDir, model)
	}

	// The look for a suspend. Two seconds so the reload is under way before
	// anyone is back at the keyboard; the tick itself is two clock reads.
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			d.checkSuspend()
			d.checkLink()
		case sig := <-sigCh:
			switch sig {
			case syscall.SIGUSR1:
				if d.isRecording() {
					d.stopRecording()
				} else {
					d.startRecording()
				}
			case syscall.SIGHUP:
				d.requestModel()
			case syscall.SIGTERM, syscall.SIGINT:
				d.mu.Lock()
				d.recording = false
				d.mu.Unlock()
				// Closing the models waits for whatever is running on one,
				// and a rehearsal length can be half a minute of audio.
				d.pauseWarming()
				log.Println("Daemon stopped.")
				return
			}
		case res := <-d.loaded:
			d.finishLoad(res)
		case res := <-d.rehearsed:
			d.finishBucket(res)
		}
	}
}

// recorder is what the daemon needs from the microphone, which is less than
// the microphone does. Named as an interface so that the dictation path --
// press, speak, press, type -- can be tested without a sound card: it is the
// most used code in the program and had no test at all, because the capture
// buffer cannot be filled from outside internal/audio.
type recorder interface {
	Start()
	Stop() []int16
	Rebuild() error
	Close()
}

type daemon struct {
	// The one model resident, and where it was loaded from. One, because a
	// model this size is most of a laptop card: they were kept so switching
	// back was instant, and holding 3.4 GiB of a shared 8 for a model nobody
	// is using costs more than a reload does.
	model     *asr.Model
	recorder  recorder
	cfg       *config.Config
	startedAt time.Time
	modelDir  string

	// loaded carries a model loaded off the main loop back to it, since that
	// loop owns every field here. loading is what is being loaded now, cancel
	// stops it, and wanted is a model asked for while that was still in
	// flight, which is also what says the load has been cancelled.
	loaded  chan loadResult
	loading string
	cancel  context.CancelFunc
	wanted  string

	// warming is the model in use rehearsing, one bucket at a time, and
	// rehearsed carries each finished bucket back to the main loop.
	warming   *rehearsal
	rehearsed chan bucketResult

	// busy is closed when the background run holding the model finishes,
	// whether that is a warmup bucket or the run that wakes the GPU at the
	// start of a recording. A model is single-threaded, so it is what
	// anything else that wants one waits on.
	busy <-chan struct{}
	// bucket says a rehearsal length is out with a goroutine and has not been
	// counted yet. Distinct from busy because settle stops waiting the moment
	// a run ends, which is before the main loop has read its result: starting
	// the next length there would leave two results for one index, and the
	// count would skip a length.
	bucket bool

	// probing says the run in flight is the wake run, and probe is what it
	// made of the speech it was given. The goroutine writes probe and the main
	// loop reads it once busy has closed, which is what orders the two.
	// answered says the model in use has had words for that clip at least
	// once, so an empty answer now is a change rather than how it always was.
	probing  bool
	probe    string
	answered bool

	// failed says a press of the dictation key did not put words on screen:
	// no model to transcribe with, or text that could not be typed. It is
	// what the bar shows when nothing else is happening. Written and read on
	// the main loop, like everything else in this struct.
	failed bool

	// asleep is the machine's suspend total as of the last look, and suspends
	// counts the sleeps noticed. A load carries the count it started under, so
	// one that read the card before a sleep is thrown away rather than
	// installed; see finishLoad.
	asleep   time.Duration
	suspends int

	// The bluetooth audio link the microphone arrives on. linkSeen says one
	// has existed this session, which is what distinguishes a headset whose
	// link has died from a machine that never had bluetooth; linkGone says
	// this disappearance has already been answered, so a headset switched off
	// on purpose is not rebuilt at every tick. linkWatch goes false if the
	// adapters cannot be read at all.
	linkWatch bool
	linkSeen  bool
	linkGone  bool

	mu        sync.Mutex
	recording bool
}

// rehearsal is one model's warmup: the lengths left to run, what the ones
// already run cost, and how to stop the one running now.
type rehearsal struct {
	model   *asr.Model
	dir     string
	buckets []int
	next    int
	work    []string
	spent   time.Duration
	cancel  context.CancelFunc
}

// bucketResult is one finished rehearsal length. It carries the model it ran
// on, since by the time it lands the daemon may have moved to another one.
type bucketResult struct {
	model *asr.Model
	// done is the run's own channel, so the daemon can tell whether the run
	// it is holding is still this one before letting go of it.
	done     <-chan struct{}
	secs     int
	took     time.Duration
	compiled uint64
	// text is what the model made of the rehearsal's known speech, which is
	// what says it is working at all. See checkProbe.
	text string
	err  error
}

// loadResult is a finished background load, successful or not. gen is the
// daemon's suspend count when the load began: a result from before the latest
// sleep may describe memory the card has since dropped.
type loadResult struct {
	dir   string
	model *asr.Model
	err   error
	took  time.Duration
	gen   int
	// woke is what the rehearsal before the load cost, which is the only read
	// on the card's power state available from here. Vulkan reports no clocks
	// and ggml reports none either, so the measurement is a fixed graph on
	// fixed audio: the same second of speech costs around 150ms on a card at
	// its clocks and near a second on one that has been left alone. It is
	// beside the load in the log because together they say whether a slow load
	// was a cold card, and on unfamiliar hardware that is the whole question.
	woke time.Duration
}

func (d *daemon) publishModel() {
	if err := ipc.Write(modelPath, []byte(d.modelDir), 0644); err != nil {
		log.Printf("model publish: %v", err)
	}
}

// requestModel acts on the model named in the model file. That file is a
// request on the way in and a statement of fact on the way out, so it is put
// back to the model actually loaded straight away: a switch is not a switch
// until the new model can transcribe, and a 2 GB model takes tens of seconds
// to get there.
func (d *daemon) requestModel() {
	raw, err := os.ReadFile(modelPath)
	if err != nil {
		log.Printf("model request: %v", err)
		return
	}
	dir := strings.TrimSpace(string(raw))
	d.publishModel()
	d.switchTo(dir)
}

// step is what a switch request does about the model asked for.
type step int

const (
	stepNothing step = iota // it is the model in use
	stepWait                // it is the model already being loaded
	stepCancel              // something else is loading: stop that and take this
	stepLoad                // load it in the background
)

func (s step) String() string {
	return [...]string{"nothing", "wait", "cancel", "load"}[s]
}

// plan decides which of those a request for req is, given the model in use,
// what is being loaded, and whether that load has already been cancelled in
// favour of something else.
//
// A newer request cancels the load in flight rather than queueing behind it.
// Queueing meant that asking for a third model while a 2 GB second one was
// loading waited out the whole of a load nobody wanted any more before the
// wanted one started.
//
// Split from switchTo so the ordering can be tested without a card: the
// already-in-use and already-loading cases have to come before the cancel, or
// a switch back to the model in use would kill a load it has no quarrel with.
func plan(req, inUse, loading string, cancelled bool) step {
	switch {
	case req == inUse:
		return stepNothing
	// Asking twice for the model already on its way. Worth its own case: the
	// published file names the model still in use, so a second ask looks new,
	// and cancelling it would throw away the load it asked for. Unless that
	// load is already cancelled, in which case the ask is for a model nothing
	// is going to produce, and it has to start again.
	case req == loading && !cancelled:
		return stepWait
	case loading != "":
		return stepCancel
	}
	return stepLoad
}

// switchTo installs a model that is already resident, or starts loading one
// that is not. The load runs off the main loop, because the daemon's whole
// job is to answer a keypress: waiting for a load here would mean pressing to
// talk and getting nothing until it finished. The old model keeps serving in
// the meantime, and the loaded model arrives back on d.loaded, which is read
// by the same loop that owns every field here.
//
// The cost of that: a transcription started mid-load shares the card with the
// load, so it is slower than it would be alone, and asr.Load measures a
// model's size from the device's free memory either side of it, which a
// concurrent transcription inflates. Both are worth a responsive keypress.
func (d *daemon) switchTo(dir string) {
	// A load whose cancel is spent is one that is still winding down, and it
	// is not going to produce anything.
	cancelled := d.loading != "" && d.cancel == nil
	switch plan(dir, d.modelDir, d.loading, cancelled) {
	case stepNothing:
		log.Printf("Already using %s", dir)
		d.dropLoad("")
	case stepWait:
		log.Printf("Already loading %s", dir)
	case stepCancel:
		// One load at a time: two at once would compete for the same card.
		// The load gives up at its next warmup bucket and drops what it had,
		// and finishLoad starts this one when it reports back.
		d.dropLoad(dir)
	case stepLoad:
		d.startLoad(dir)
	}
}

// startLoad begins loading a model off the main loop, unconditionally: the
// decision that a load is the right answer belongs to switchTo, except after a
// resume, when the model to load is the one nominally in use and plan would
// call it nothing to do.
func (d *daemon) startLoad(dir string) {
	ctx, cancel := context.WithCancel(context.Background())
	d.loading, d.cancel = dir, cancel
	gen := d.suspends
	// Bring the card's clocks up before the weights go over, not during. A
	// load is thousands of small transfers and not one of them is long enough
	// to clock the card up by itself, so on a card left idle the whole upload
	// runs at the bottom of its power curve. Measured on a laptop RTX 4070,
	// parakeet-tdt-0.6b took 183ms loaded straight after a rehearsal and
	// 1m50.8s after thirty seconds of quiet, with everything else equal; one
	// run in between brought it back to 590ms. Nothing is wrong with the
	// card or the link, it is asleep, and this is the same rehearsal the daemon
	// already runs before a dictation for the same reason.
	//
	// This is a graph run, not a delay: the goroutine below waits for it to
	// finish rather than for any interval. What it costs is what that card
	// needed, ~130ms when it was already at its clocks and ~880ms when not.
	//
	// Nothing to wake it with is fine: a bucket already in flight means the
	// card is busy, and no model at all means this is the first load of the
	// session, which follows the backend's own device init.
	wakeStart := time.Now()
	woke := d.wakeRun(nil)
	d.restoreStatus()
	log.Printf("Loading %s in the background", dir)
	go func() {
		// The clocks have to be up before the upload starts, not alongside it,
		// so this waits rather than racing.
		var wokeFor time.Duration
		if woke != nil {
			<-woke
			wokeFor = time.Since(wakeStart)
		}
		t0 := time.Now()
		model, err := asr.Load(dir)
		// Cancelled while it was reading: nobody is waiting for this
		// model any more, and it is holding memory the one they did ask
		// for is about to want.
		if err == nil && ctx.Err() != nil {
			model.Close()
			model, err = nil, ctx.Err()
		}
		d.loaded <- loadResult{dir: dir, model: model, err: err, took: time.Since(t0), gen: gen, woke: wokeFor}
	}()
}

// checkLink notices that the bluetooth link the microphone arrives on has
// gone, and rebuilds the audio device before anyone presses the key. Without
// it the loss is found by dictating into a dead microphone, which costs that
// dictation; the link is checked between them, so it costs nothing.
//
// The transition is what is watched, not the count. Zero synchronous links is
// also the answer on every machine with no bluetooth and on every headset that
// is merely connected, so only losing one that existed means anything. It is
// answered once: a headset switched off on purpose stays off, and rebuilding
// at every tick would fight whoever switched it.
//
// A rebuild for the wrong reason is cheap here, which is why this can afford
// to be eager. It happens between dictations and costs a couple of seconds of
// a microphone nobody is using, and reopening also picks up whatever the
// default input became, which after a headset is switched off is the answer
// anyway.
func (d *daemon) checkLink() {
	// Not during a dictation: Rebuild closes the device the capture is
	// arriving on. What broke mid-recording is caught when it ends.
	if !d.linkWatch || d.isRecording() {
		return
	}
	n, err := sco.Links()
	if err != nil {
		// Said once. There is no bluetooth microphone to lose on a machine
		// whose adapters cannot be read, and a line every two seconds would
		// bury the log.
		log.Printf("Cannot read the bluetooth link state, no longer watching it: %v", err)
		d.linkWatch = false
		return
	}
	if n > 0 {
		d.linkSeen = true
		d.linkGone = false
		return
	}
	if !d.linkSeen || d.linkGone {
		return
	}
	d.linkGone = true
	log.Printf("The microphone's bluetooth link is gone: rebuilding the audio device.")
	if err := d.recorder.Rebuild(); err != nil {
		log.Printf("Rebuilding the audio device failed: %v", err)
	}
}

// checkSuspend notices that the machine has slept since the last look and
// reloads the model, because the card's memory may not have survived: unless
// the driver was told to preserve video memory, the weights are discarded and
// the model runs its graphs at full speed returning nothing. Reloading on
// every resume costs a few background seconds on a machine whose driver did
// preserve them, and is the only answer that needs nothing from the driver on
// the machines where it did not.
//
// Polled off a ticker rather than told by logind, so it works the same with
// no D-Bus and no systemd, and it counts sleeps nothing orchestrated. The
// ticker does not tick while the machine is suspended, so the look after a
// resume comes within one period of it.
func (d *daemon) checkSuspend() {
	total := suspend.Total()
	// Half a second of slack for reading two clocks a syscall apart; a real
	// suspend is seconds at the least.
	if total-d.asleep < 500*time.Millisecond {
		return
	}
	slept := total - d.asleep
	d.asleep = total
	d.suspends++
	// A model on the CPU sleeps in RAM, which suspend preserves by definition.
	if d.model == nil || !d.model.OnGPU() {
		return
	}
	log.Printf("Machine was suspended for %s; the card may have dropped what it held",
		slept.Round(time.Second))
	if d.loading != "" {
		// The load in flight began before the sleep, so finishLoad throws its
		// model away and starts it over; a second load now would race it for
		// the card.
		return
	}
	d.startLoad(d.modelDir)
}

// dropLoad stops the load in flight and records what to load instead, or ""
// for nothing. Every path through switchTo ends up here, because a switch
// answers the most recent request and the card should not still be loading a
// model nobody is waiting for. Even the two that need no load of their own:
// letting one run on would take the model the user just chose back off them
// tens of seconds later.
//
// Spending the cancel is what says the load is already dying, so a later
// request for the same model knows to ask again rather than wait for
// something nothing is going to produce.
func (d *daemon) dropLoad(next string) {
	if d.loading == "" {
		return
	}
	if d.cancel != nil {
		debugf("Cancelling the load of %s", d.loading)
		d.cancel()
		d.cancel = nil
	}
	d.wanted = next
}

// finishLoad installs what the background load produced, and then honours a
// switch asked for while it was running.
func (d *daemon) finishLoad(res loadResult) {
	d.loading, d.cancel = "", nil
	switch {
	case errors.Is(res.err, context.Canceled):
		log.Printf("Gave up on %s after %s", res.dir, res.took.Round(time.Millisecond))
	case res.err != nil:
		// Keep serving with the model we have. The published model still
		// names it, since a request never overwrote it.
		log.Printf("model load %s: %v", res.dir, res.err)
	case res.gen != d.suspends:
		// The machine slept while this load was reading the card, so what it
		// loaded may already be gone. Installing it would put a model that
		// looks fresh but may be mute where the whole point of the reload was
		// certainty; do it again. Unless something newer was asked for, which
		// the lines below start, and which replaces this model anyway.
		res.model.Close()
		if d.wanted == "" {
			log.Printf("The machine slept while %s was loading; loading it again", res.dir)
			d.startLoad(res.dir)
		}
	default:
		d.install(res.dir, res.model)
		// The total stays at the default level, since a switch is something
		// the user asked for and a two minute one has to be visible without
		// anyone knowing to turn anything on. Which part of it was slow is the
		// next question rather than the first, so the split waits for debug.
		log.Printf("Model now %s in %s, %s resident",
			res.model.Arch(), res.took.Round(time.Millisecond),
			human.Bytes(res.model.Bytes()))
		debugf("Load: woke %s, %s", res.woke.Round(time.Millisecond), res.model.LoadTimings())
	}
	// A request made during the load, unless it asked for what the load just
	// installed, which is what asking twice for a slow model looks like.
	next := d.wanted
	d.wanted = ""
	if next != "" && next != d.modelDir {
		d.switchTo(next)
	}
	// Nothing rehearsed while the load had the card, so the model in use may
	// have buckets left. Declines by itself if the line above started another
	// load.
	d.warmNext()
	// The bar is how anyone knows a load is still running, so it has to be put
	// back on every way one can end. Leaving it to install meant a load that
	// failed or was cancelled left LOAD lit until something else happened to
	// write the status, which for a daemon that is idle by design is the next
	// dictation, minutes later.
	d.restoreStatus()
}

// install makes a freshly loaded model the one in use and frees the one it
// replaces. Recording is not interrupted: the capture buffer is independent
// of the model, so a swap while armed just means the new model transcribes
// what was captured.
func (d *daemon) install(dir string, model *asr.Model) {
	// Whatever is rehearsing is rehearsing the model being replaced, and it
	// cannot be closed until it lets go of the card.
	d.pauseWarming()
	d.settle()
	if d.model != nil {
		d.model.Close()
	}
	// A daemon that had nothing to transcribe with has something now, so the
	// bar stops saying the last press failed. Only from nothing: a switch does
	// not undo a dictation that could not be typed, and the tool that could
	// not type it is still missing.
	if d.model == nil {
		d.failed = false
	}
	d.model, d.modelDir = model, dir
	// What the model it replaces had words for says nothing about this one.
	d.answered = false
	d.publishModel()
	d.startWarming(dir, model)
	d.restoreStatus()
}

// startWarming begins the rehearsal of the model now in use. Nothing carries
// over from the model before it: that one has been freed, and what it had run
// went with it.
func (d *daemon) startWarming(dir string, model *asr.Model) {
	d.warming = &rehearsal{model: model, dir: dir, buckets: warmup.Buckets(model)}
	if len(d.warming.buckets) == 0 {
		d.warming = nil
		return
	}
	d.warmNext()
}

// warmNext runs one bucket, if this is a moment to run one. It is called from
// every place that could have made it one, so the rehearsal creeps forward in
// the gaps between everything else the daemon does.
//
// Never while recording: a model is single-threaded, so a bucket in flight is
// something the transcription of what is being said right now would have to
// wait behind, and the last bucket is half a minute of audio. Never while
// another model is loading either, since both want the same card.
func (d *daemon) warmNext() {
	w := d.warming
	if w == nil || d.busy != nil || d.bucket || d.loading != "" || d.isRecording() {
		return
	}
	if w.next == len(w.buckets) {
		debugf("Warmed %s in %s: %s, %s resident, good for %s of audio",
			w.model.Name(), w.spent.Round(time.Millisecond), strings.Join(w.work, " "),
			human.Bytes(w.model.Bytes()), w.model.AudioLimit().Round(time.Second))
		d.warming = nil
		return
	}
	secs := w.buckets[w.next]
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	done := make(chan struct{})
	d.busy, d.bucket = done, true
	model := w.model
	go func() {
		defer close(done)
		t0 := time.Now()
		compiled, text, err := warmup.Bucket(ctx, model, secs)
		d.rehearsed <- bucketResult{model: model, done: done, secs: secs,
			took: time.Since(t0), compiled: compiled, text: text, err: err}
	}()
}

// finishBucket records what a rehearsal length cost and goes on to the next.
func (d *daemon) finishBucket(res bucketResult) {
	d.bucket = false
	// Only if the daemon is still holding this run. A recording that settled
	// while the result was in flight may have started a wake run since, and
	// that one has not finished.
	if d.busy == res.done {
		d.busy = nil
	}
	// A rehearsal runs the same known speech the wake run does, and it runs
	// within a second or two of every load. Taking the baseline from it rather
	// than from the first dictation is what covers the machine suspended
	// before anyone dictated: without it the model has never answered, so the
	// silence after the resume is not a change from anything and the daemon
	// would stay mute for the rest of the session.
	if res.model == d.model && res.text != "" {
		d.answered = true
	}
	if w := d.warming; w != nil && w.model == res.model && !w.record(res) {
		log.Printf("warmup: %v", res.err)
		d.warming = nil
	}
	d.warmNext()
	d.restoreStatus()
}

// record folds a finished length into the rehearsal and says whether it is
// worth going on with.
//
// A length that was cancelled keeps its place: something the user was waiting
// for wanted the model, and the run may have stopped before the shape it was
// there to compile. A length that failed ends the rehearsal instead, since a
// bucket that fails on this model fails every time and retrying it is a busy
// loop on the card.
func (w *rehearsal) record(res bucketResult) bool {
	if res.err != nil {
		return errors.Is(res.err, asr.ErrAborted)
	}
	w.work = append(w.work, warmup.Report(res.secs, res.took, res.compiled))
	w.spent += res.took
	w.next++
	return true
}

// pauseWarming gives up the bucket in flight without giving up the rehearsal.
// The length it was on is run again later, since a cancelled run may have
// stopped before the shape it was there to compile.
func (d *daemon) pauseWarming() {
	if d.warming != nil && d.warming.cancel != nil {
		d.warming.cancel()
	}
}

func (d *daemon) closeModel() {
	d.settle()
	if d.model != nil {
		d.model.Close()
	}
}

// restoreStatus puts the bar back to whatever is true now that whatever it
// was showing is over, and says what is happening in the activity file for
// `diktat model` to read. A load in the background outranks idle but not
// recording: it is worth knowing a switch is pending, and worth more to know
// the mic is live.
//
// A rehearsal is deliberately not on the bar. The bar says what stops someone
// dictating, and a model still rehearsing does not: it transcribes, a little
// slower at a length it has not met yet. The activity file carries it instead,
// where it is read by whoever asked.
func (d *daemon) restoreStatus() {
	switch {
	case d.isRecording():
		setStatus(statusRec)
	case d.loading != "":
		setStatus(statusLoad)
	case d.failed:
		setStatus(statusErr)
	default:
		setStatus("")
	}
	d.publishActivity()
}

// publishActivity writes what the daemon is busy with, as a word and the
// model it applies to, or removes the file when it is busy with nothing.
//
// Separate from the status file because that one is Pango markup for a bar
// and names no model. `diktat model` is where someone asks which model they
// have, and until this it could only answer for the one already in use, which
// is exactly the wrong half during a switch.
func (d *daemon) publishActivity() {
	line := ""
	switch {
	case d.loading != "":
		line = "loading " + d.loading
	case d.warming != nil:
		line = "warming " + d.warming.dir
	}
	if line == "" {
		os.Remove(activityPath)
		return
	}
	if err := ipc.Write(activityPath, []byte(line), 0644); err != nil {
		log.Printf("activity: %v", err)
	}
}

func (d *daemon) isRecording() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.recording
}

// The bar is written first on both toggles, before anything else the press
// sets off. The press is the only thing the light is reporting, so what the
// daemon happens to be doing when it arrives may not delay it: everything
// below is a cancel, a goroutine or a mutex today, and the guarantee should
// not rest on that staying true.
func (d *daemon) startRecording() {
	setStatus(statusRec)
	// Whatever went wrong last time, this press is about this dictation. A
	// missing model puts it back within the second, from the press itself.
	d.failed = false
	d.startedAt = time.Now()
	d.recorder.Start()
	d.mu.Lock()
	d.recording = true
	d.mu.Unlock()
	// Whatever the rehearsal is on, this utterance wants the model more. The
	// bucket gives up where it can, which is not instantly, and the wake run
	// below stands aside if it is still going: a bucket is a graph run too,
	// so it wakes the card just as well.
	d.pauseWarming()
	d.wake()
	debugf("Recording...")
}

// wake spends the time someone is speaking on a throwaway run, because a card
// left alone drops its clocks and the next graph pays to bring them back. That
// cost lands on the user: on granite, an utterance 25 seconds after the last
// one encoded in 993ms where the same utterance back to back encoded in 27ms.
// A single short run absorbs it, and it has seconds of speech to hide behind.
//
// The run is a length the warmup already rehearsed, so it compiles nothing.
// Errors are ignored: this is an optimisation, and the transcription that
// follows will report anything real.
//
// What it says is kept rather than thrown away. This is the only run all
// session on audio whose words are known in advance, which makes checking the
// answer a free test that the model still works; see checkProbe.
func (d *daemon) wake() {
	d.probing = d.wakeRun(&d.probe) != nil
}

// wakeRun starts one second of the known speech on the model in use and
// returns the channel that closes when it ends, or nil when there is nothing
// to run it on or something is already running. text, when given, receives
// what came back once that channel closes.
//
// Two callers want this for the same reason and keep different halves of it.
// A recording wants the clocks up and also wants the transcript, since this is
// the only audio all session whose words are known in advance; a model load
// wants only the clocks.
func (d *daemon) wakeRun(text *string) <-chan struct{} {
	if d.model == nil || !d.model.OnGPU() || d.busy != nil {
		return nil
	}
	speech, err := warmup.Speech()
	if err != nil {
		return nil
	}
	done := make(chan struct{})
	d.busy = done
	model := d.model
	go func() {
		defer close(done)
		out, _ := model.Transcribe(context.Background(), warmup.Fit(speech, 1))
		if text != nil {
			*text = out
		}
	}()
	return done
}

// checkProbe reloads the model if the wake run says it has stopped working.
//
// A model whose weights are no longer in the card's memory does not fail: it
// runs its graphs at the usual speed and returns nothing at all, on every
// utterance, until something reloads it. That is what a suspend does here,
// since the contents of video memory do not survive one unless the driver was
// told to save them, and the daemon holds its model across the whole session
// by design. Before this, every dictation after a resume typed nothing and the
// log gave no reason beyond "0 chars".
//
// A suspend itself is noticed directly, by checkSuspend, whose reload is
// under way before anyone is back at the keyboard. This is the backstop
// behind it: a dictation begun inside that reload, and video memory lost to
// anything that is not a suspend, which nothing else would notice.
func (d *daemon) checkProbe() {
	if !d.probing {
		return
	}
	broken := mute(d.probe, d.answered)
	d.probing, d.answered = false, d.answered || d.probe != ""
	if !broken {
		return
	}

	// On the main loop, unlike every other load here, and deliberately: the
	// model in use cannot transcribe, so there is nothing to answer a keypress
	// with in the meantime. The capture is already in hand and is transcribed
	// by the new model, so the dictation that found this is not lost.
	if d.loading == d.modelDir {
		// checkSuspend saw the sleep too and its reload is already on its way;
		// a second copy of the same model would race it for the card. Wait it
		// out here instead, for the same reason the load below is synchronous.
		log.Printf("Model said nothing to speech it has words for; waiting out the reload in flight")
		d.finishLoad(<-d.loaded)
		if d.loading != "" {
			// The reload gave way to a newer request; let that one land.
			return
		}
	} else {
		log.Printf("Model said nothing to speech it has words for; reloading %s", d.modelDir)
		model, err := asr.Load(d.modelDir)
		if err != nil {
			log.Fatalf("reload %s: %v", d.modelDir, err)
		}
		d.install(d.modelDir, model)
	}
	// install puts the bar back to what the daemon's state says, which during
	// this is a recording that has already stopped. The wait is still the one
	// the caller started, so say so again.
	setStatus(statusTx)

	// Whether a fresh load into the same process is enough is not something
	// the daemon can know: it recreates the model, not the device it lives on.
	// If the reload is still mute the process is what has to go, and the unit
	// restarts it, which is the recovery that was known to work by hand.
	speech, err := warmup.Speech()
	if err != nil {
		return
	}
	text, err := d.model.Transcribe(context.Background(), warmup.Fit(speech, 1))
	if err != nil {
		log.Fatalf("probe %s: %v", d.modelDir, err)
	}
	if text == "" {
		log.Fatalf("Reloaded %s is still mute; restarting.", d.modelDir)
	}
	d.answered = true
}

// mute reports whether the wake run's answer says the model has stopped
// working: nothing back from a clip it has had words for before.
//
// A model that has never answered the clip is not judged. It is a second of
// the same synthesised sentence every time, so a model with words for it once
// has words for it always; one that never had any would otherwise be reloaded
// before every dictation for as long as it stayed in use.
func mute(probe string, answered bool) bool {
	return probe == "" && answered
}

// settle waits for the background run holding the model to finish. A model is
// single-threaded, and both the wake run and a warmup bucket hold one, so
// nothing else may touch a model until whichever it is has let go.
func (d *daemon) settle() {
	if d.busy == nil {
		return
	}
	<-d.busy
	d.busy = nil
}

// TX covers everything between the press and the text: waiting out the wake
// run, reloading a model that has gone quiet, and the transcription itself.
// From outside, all of it is one wait, and REC through any of it says the mic
// is live when it is not. The recording flag does not move up with the status,
// since it is also what keeps a rehearsal from starting on the model this
// transcription is about to want.
func (d *daemon) stopRecording() {
	setStatus(statusTx)
	samples := d.recorder.Stop()
	d.settle()
	d.checkProbe()
	d.mu.Lock()
	d.recording = false
	d.mu.Unlock()
	// However this ends, the bar goes back to what is true afterwards and the
	// rehearsal picks up where the recording interrupted it. Both were easy
	// to miss on one of the ways out: a silent capture used to write an empty
	// status directly, which turned a load still running into an idle bar.
	defer d.restoreStatus()
	defer d.warmNext()

	if len(samples) == 0 {
		log.Println("No audio.")
		return
	}
	peak, rms := audio.Levels(samples)
	silent := audio.IsSilent(samples)
	// One gain for the whole capture, applied to each piece as it is
	// converted, so a recording split into chunks does not change loudness
	// halfway through.
	gain := audio.Gain(samples)
	// Audio duration is derived from the sample count at the rate we asked the
	// device for. If it drifts from the wall clock, the device is not actually
	// giving us that rate, and the model is seeing time-stretched speech.
	debugf("Transcribing %.1fs (wall %.1fs, peak %.3f rms %.4f gain %.1fx)...",
		float64(len(samples))/float64(audio.SampleRate), time.Since(d.startedAt).Seconds(),
		peak, rms, gain)

	// Not one bit set in the whole capture. Either the input is gone, which
	// on a bluetooth headset outlives the dictation and every one after it,
	// or nobody spoke. Rebuilding the device answers the first and costs the
	// second a few seconds of a microphone that was not in use, which is the
	// cheap way round: the expensive way was a session of dictations that
	// typed nothing and a log that said only "0 chars".
	if audio.IsDead(samples) {
		log.Printf("Nothing came through the microphone: rebuilding the audio device.")
		if err := d.recorder.Rebuild(); err != nil {
			log.Printf("Rebuilding the audio device failed: %v", err)
		}
		// The press produced nothing and the reason was not the speaker, so
		// the bar says so. A capture that is merely silent does not: somebody
		// pressing the key and saying nothing is an ordinary thing to do, and
		// a light that scolds them for it is noise.
		d.failed = true
		return
	}

	// Nothing was said. Every family invents something when asked to
	// transcribe silence, and the inventions cost more than the check does.
	if silent {
		log.Printf("Nothing to transcribe: the capture is silent.")
		return
	}

	// No model means the load failed and there was nothing already resident to
	// fall back to. Everything below this dereferences the model, so without
	// this the second press of the dictation key takes the daemon down with a
	// nil pointer -- and the unit restarts it, into the same failed load, so
	// the crash repeats every time somebody tries to dictate.
	//
	// After the silence checks rather than before them: a microphone that has
	// gone silent is worth repairing whether or not there is a model to
	// transcribe with, since the repair is what makes the next dictation
	// possible once there is one.
	if d.model == nil {
		log.Printf("No model is loaded, so there is nothing to transcribe with. "+
			"Choose one with: diktat model %s", config.StartModel())
		d.failed = true
		return
	}

	t0 := time.Now()
	kernels := d.model.CompiledKernelNames()
	// Only what the model would refuse, or what the card cannot hold, gets cut,
	// at the quietest moment near the limit. Most families take the whole
	// utterance and window it themselves, which they do better than a cut here
	// can: cutting at 30s cost a broken sentence at every seam even on models
	// that had no limit at all.
	limit := d.model.AudioLimit()
	chunks := audio.Chunk(samples, int(limit.Seconds())*audio.SampleRate)
	if len(chunks) > 1 {
		log.Printf("Over the %s this model can take now, transcribing in %d pieces",
			limit.Round(time.Second), len(chunks))
	}
	var parts []string
	for _, chunk := range chunks {
		part, err := d.model.Transcribe(context.Background(), audio.Pad(audio.Floats(chunk, gain)))
		if err != nil {
			log.Printf("transcribe: %v", err)
			d.failed = true
			return
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	text := strings.Join(parts, " ")
	// Anything compiled here is a shape the warmup did not cover, and it is
	// the reason this transcription was slower than the next one at the same
	// length will be. Expected past the last warm bucket, a bug below it, and
	// named either way, since the name says which variant the buckets missed.
	if compiled := d.model.CompiledKernelNames()[len(kernels):]; len(compiled) > 0 {
		debugf("Compiled %d kernels mid-transcription, on %.1fs of audio: %s",
			len(compiled), float64(len(samples))/float64(audio.SampleRate),
			strings.Join(compiled, " "))
	}
	// The text itself is deliberately not logged, whatever the level: this
	// goes to the journal, which keeps it across restarts, and everything ever
	// dictated would accumulate there. Length is enough to tell "heard
	// nothing" from "heard something".
	//
	// The breakdown separates the model's own work from everything around it,
	// which is what tells a slow model from a cold one: a first utterance that
	// spends its time in encode is still compiling shaders. That is a question
	// for someone debugging, so it is not asked several times a minute.
	tm := d.model.Timings()
	debugf("Transcribed in %s (mel %s, encode %s, decode %s, other %s): %d chars",
		time.Since(t0).Round(time.Millisecond), tm.Mel.Round(time.Millisecond),
		tm.Encode.Round(time.Millisecond), tm.Decode.Round(time.Millisecond),
		tm.Other.Round(time.Millisecond), len(text))

	if text != "" {
		out := text + " "
		if path, err := ipc.LastText(); err != nil {
			log.Printf("last-text: %v", err)
		} else if err := ipc.Write(path, []byte(out), 0600); err != nil {
			log.Printf("last-text write: %v", err)
		}
		d.appendHistory(text)
		// The text is written above before this runs, so a failure here loses
		// nothing: `diktat repeat` types it again, and saying so is the whole
		// difference between a bad minute and a lost sentence.
		if err := output.Type(out, d.cfg.TypingMethods); err != nil {
			log.Printf("type: %v", err)
			log.Println("The text is kept; `diktat repeat` types it again.")
			d.failed = true
		}
	}
}

func (d *daemon) appendHistory(text string) {
	if d.cfg.HistoryFile == "" {
		return
	}
	path, err := historyPath(string(d.cfg.HistoryFile))
	if err != nil {
		log.Printf("history path: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		log.Printf("history mkdir: %v", err)
		return
	}
	// 0600: this is every sentence ever dictated, which is the same reason
	// the last-text file is kept in a mode 0700 directory.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("history open: %v", err)
		return
	}
	defer f.Close()
	// The mode above applies only when this call creates the file. A history
	// that already exists keeps whatever mode it had, so one created before
	// this rule -- or by anything else -- stays readable by everyone on the
	// machine, silently and forever.
	if info, err := f.Stat(); err == nil && info.Mode().Perm()&0o077 != 0 {
		if err := f.Chmod(0o600); err != nil {
			log.Printf("history chmod: %v", err)
		}
	}
	if err := json.NewEncoder(f).Encode(map[string]string{
		"ts":   time.Now().UTC().Format(time.RFC3339Nano),
		"text": text,
	}); err != nil {
		// A full disk loses the line either way; saying so is the difference
		// between a history with a gap and a history nobody knows has one.
		log.Printf("history write: %v", err)
	}
}

// historyPath expands a leading ~ against the home directory, which a
// hand-written config is likely to contain and nothing else here expands.
//
// Only "~" and "~/..." -- "~someone/notes" names another user's home, which
// this cannot resolve and must not quietly turn into a directory of that name
// under this user's.
func historyPath(raw string) (string, error) {
	if raw != "~" && !strings.HasPrefix(raw, "~/") {
		return raw, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("%s starts with ~ and there is no home directory: %w", raw, err)
	}
	return filepath.Join(home, strings.TrimPrefix(raw, "~")), nil
}

// debugEnabled turns on the lines that describe how the daemon did something
// rather than what it did. Off by default: dictating is the common case and it
// happened several times a minute, so the interesting lines, a switch, a
// suspend, anything that failed, were buried under level meters and per-stage
// timings from every utterance. DIKTAT_DEBUG brings them back, spelled like
// DIKTAT_GPU because that is the only other knob here.
var debugEnabled = os.Getenv("DIKTAT_DEBUG") != ""

// debugf logs a line worth having when something is wrong and worth nothing
// the rest of the time.
func debugf(format string, v ...any) {
	if debugEnabled {
		log.Printf(format, v...)
	}
}

// logFlags stamps the log with the time, unless something downstream is
// already doing it.
//
// systemd connects a service's stderr straight to the journal, which is why
// nothing here configures a log destination, and it stamps every line as it
// arrives; adding our own would print two times per line. Run by hand there is
// nothing to stamp them at all, and a log whose subject is how long things
// took is a poor one to read without times. systemd sets JOURNAL_STREAM on
// exactly the processes whose output it is capturing, so it answers which of
// the two this is, and it answers for `systemd-run` and `systemd-cat` too.
func logFlags() int {
	if os.Getenv("JOURNAL_STREAM") != "" {
		return 0
	}
	return log.LstdFlags
}

// These are resolved at startup and used from every corner of the daemon,
// including the signal handlers, which have no daemon value to hang them off.
var statusPath, modelPath, activityPath string

func setStatus(s string) {
	_ = ipc.Write(statusPath, []byte(s), 0644)
}
