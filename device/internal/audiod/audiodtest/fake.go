// Package audiodtest is a fake audiod for tests: a test binary re-execs itself
// as `<binary> --socket <path> [flags]`, and its TestMain hands over to Main.
// The fake records what it receives in the socket's directory.
package audiodtest

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

const (
	recordsFile = "recv.log"
	runsFile    = "runs"
	flagsFile   = "flags"
	exitCrash   = 3
)

// IsFake reports whether this process was started as the fake audiod.
func IsFake() bool { return len(os.Args) > 2 && os.Args[1] == "--socket" }

// Main runs the fake audiod and exits.
func Main() { os.Exit(run(os.Args[1:])) }

// Record is one envelope the fake received, in its run (1 for the first start).
type Record struct {
	Run    int
	Kind   string
	Detail string
}

// Records reads what the fakes started with a socket in dir received, in order.
func Records(dir string) []Record {
	f, err := os.Open(filepath.Join(dir, recordsFile)) //nolint:gosec // test dir
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) != 3 {
			continue
		}
		n, _ := strconv.Atoi(parts[0])
		out = append(out, Record{Run: n, Kind: parts[1], Detail: parts[2]})
	}
	return out
}

// WriteFlags makes every fake started with a socket in dir take flags too.
func WriteFlags(dir string, flags ...string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, flagsFile), []byte(strings.Join(flags, " ")), 0o600)
}

// PIDs returns the pid of each run, in order.
func PIDs(dir string) []int {
	raw, _ := os.ReadFile(filepath.Join(dir, runsFile)) //nolint:gosec // test dir
	var out []int
	for _, l := range strings.Fields(string(raw)) {
		if n, err := strconv.Atoi(l); err == nil {
			out = append(out, n)
		}
	}
	return out
}

type fake struct {
	dir            string
	run            int
	exitAfterReady bool
	crashRuns      int
	holdRun        int
	holdMS         int
	noPong         bool
	crashMidStream bool
	stderr         bool
	stderrLong     bool
	volumeFail     bool
	hang           bool
	noReady        bool
	mediaCode      string
	crashOnCommand bool
	mediaState     bool
	soloist        bool
	airplay        string
	btControl      bool
	group          string
	vendorMS       int
	vendorCode     string
	wake           bool
	noMic          bool
	toneStopCrash  bool

	recMu sync.Mutex
}

func run(args []string) int {
	fs := flag.NewFlagSet("fake-audiod", flag.ContinueOnError)
	socket := fs.String("socket", "", "")
	f := &fake{}
	fs.BoolVar(&f.exitAfterReady, "exit-after-ready", false, "exit with code 3 right after Ready")
	fs.IntVar(&f.crashRuns, "crash-runs", 0, "exit with code 3 right after Ready in runs 1 to this one")
	fs.IntVar(&f.holdRun, "hold-run", 0, "the run that waits hold-ms after Ready before it exits")
	fs.IntVar(&f.holdMS, "hold-ms", 0, "")
	fs.BoolVar(&f.noPong, "no-pong", false, "never answer a HealthPing")
	ignoreTerm := fs.Bool("ignore-term", false, "ignore SIGTERM")
	fs.BoolVar(&f.crashMidStream, "crash-mid-stream", false, "exit with code 3 at the first AudioDown of run 1")
	fs.BoolVar(&f.stderr, "stderr", false, "write stderr lines and LogLines, good and bad")
	fs.BoolVar(&f.stderrLong, "stderr-long", false, "write a 5000-byte stderr line, a short one, and one stderr ends without its newline")
	fs.BoolVar(&f.volumeFail, "volume-fail", false, "answer VolumeSet with ok false")
	fs.BoolVar(&f.hang, "hang", false, "stop reading the socket after the first VolumeSet")
	fs.BoolVar(&f.noReady, "no-ready", false, "answer Configure with nothing")
	fs.StringVar(&f.mediaCode, "media-code", "", "answer every UrlPlay and MediaCommand with ok false and this code")
	fs.BoolVar(&f.crashOnCommand, "crash-on-command", false, "exit with code 3 at the first UrlPlay or MediaCommand of run 1, unanswered")
	fs.BoolVar(&f.mediaState, "media-state", false, "send an idle MediaState and a VizFrame after Ready, and a playing one with the request's metadata after an answered UrlPlay")
	fs.BoolVar(&f.soloist, "soloist", false, "answer a SoloistConfig with a no_build SoloistStatus, the first SenderVolumeSet with a SenderVolume 5 above it, and a SoloistProbe with a result")
	fs.StringVar(&f.airplay, "airplay", "", "answer an AirplayConfig with an AirplayStatus: ready, or failed with this code")
	fs.BoolVar(&f.btControl, "bluetooth-control", false, "answer a BluetoothStream with a BluetoothControl pause")
	fs.StringVar(&f.group, "group", "", "answer an enabled GroupConfig with a GroupStatus in this state (playing, failed, ...), echoing its group and role, with one member for a leader")
	fs.IntVar(&f.vendorMS, "vendor-ms", 0, "send VendorStatus{engines true} this long after Ready, or {engines false, disabled} for engines the Configure turned off; a capture started before it waits for it")
	fs.StringVar(&f.vendorCode, "vendor-code", "", "the VendorStatus code")
	fs.BoolVar(&f.wake, "wake", false, "send a WakeEvent and a CommandWord{id 2} after Ready, after the VendorStatus if one goes")
	fs.BoolVar(&f.noMic, "no-mic", false, "send no MicOpus for a capture")
	fs.BoolVar(&f.toneStopCrash, "crash-on-tone-stop", false, "exit with code 3 at the first ToneStop of run 1, unanswered")
	noListen := fs.Bool("no-listen", false, "never listen on the socket")
	if err := fs.Parse(args); err != nil || *socket == "" {
		return 2
	}
	// a board under test runs the fake without its flags; they wait beside the socket
	if raw, err := os.ReadFile(filepath.Join(filepath.Dir(*socket), flagsFile)); err == nil { //nolint:gosec // test dir
		if err := fs.Parse(strings.Fields(string(raw))); err != nil {
			return 2
		}
	}
	f.dir = filepath.Dir(*socket)
	f.run = f.nextRun()

	if *ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	} else {
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		go func() {
			<-term
			f.record("term", "")
			os.Exit(0)
		}()
	}
	// the sim's fake group clock, which only the sim board passes on
	if off, ppm := os.Getenv("CADENCE_SIM_CLOCK_OFFSET_MS"), os.Getenv("CADENCE_SIM_CLOCK_PPM"); off != "" || ppm != "" {
		f.record("sim_clock_env", fmt.Sprintf("offset_ms=%q ppm=%q", off, ppm))
	}
	if f.stderr {
		writeStderr()
	}
	if f.stderrLong {
		_, _ = os.Stderr.WriteString(`{"level":"INFO","msg":"fake_huge","pad":"` + strings.Repeat("y", 5000) + "\"}\n" +
			`{"level":"INFO","msg":"fake_after_huge"}` + "\n" + `{"level":"INFO","msg":"fake_unended"}`)
	}

	if *noListen {
		select {}
	}
	_ = os.Remove(*socket)
	ln, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: *socket, Net: "unixpacket"})
	if err != nil {
		return 1
	}
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			return 1
		}
		f.serve(c)
	}
}

// nextRun appends this process's pid to the runs file; its line is the run number
func (f *fake) nextRun() int {
	fh, err := os.OpenFile(filepath.Join(f.dir, runsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test dir
	if err != nil {
		os.Exit(1)
	}
	_, _ = fmt.Fprintf(fh, "%d\n", os.Getpid())
	_ = fh.Close()
	return len(PIDs(f.dir))
}

func (f *fake) record(kind, detail string) {
	f.recMu.Lock()
	defer f.recMu.Unlock()
	fh, err := os.OpenFile(filepath.Join(f.dir, recordsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test dir
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(fh, "%d\t%s\t%s\n", f.run, kind, detail)
	_ = fh.Close()
}

func writeStderr() {
	lines := []string{
		`{"level":"WARN","msg":"fake_event","count":3,"device":"card0"}`,
		// audiod's own volume lines carry a second, numeric level
		`{"level":"INFO","msg":"fake_volume","level":40,"ratio":0.5}`,
		`libasound: SECRET_RAW`,
		`{"level":"INFO","msg":"fake_long","pad":"SECRET_LONG` + strings.Repeat("x", 1100) + `"}`,
		`{"level":"INFO","msg":"fake_nested","obj":{"k":"SECRET_NESTED"}}`,
	}
	for _, l := range lines {
		_, _ = os.Stderr.WriteString(l + "\n")
	}
}

type conn struct {
	c  *net.UnixConn
	mu sync.Mutex
}

func (c *conn) send(env *audiodv1.Envelope) {
	b, err := proto.Marshal(env)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.c.Write(b)
}

func (f *fake) serve(uc *net.UnixConn) {
	defer func() { _ = uc.Close() }()
	c := &conn{c: uc}
	var capturePath string
	// the Configure turned the vendor engines off, as safe mode does
	enginesOff := false
	// closed once the vendor verdict went out: captures wait for it, as audiod's do during the probe
	verdict := make(chan struct{})
	captures := map[string]chan struct{}{}
	defer func() {
		for _, stop := range captures {
			close(stop)
		}
	}()
	buf := make([]byte, 20000)
	// --soloist moves the slider once per connection
	moved := false
	// the ToneStart id that sounds, "" for none: as audiod, a transient VolumeSet is refused while none does
	ringing := ""
	for {
		n, err := uc.Read(buf)
		if err != nil || n == 0 {
			return
		}
		env := &audiodv1.Envelope{}
		if err := proto.Unmarshal(buf[:n], env); err != nil {
			return
		}
		switch b := env.GetBody().(type) {
		case *audiodv1.Envelope_Configure:
			capturePath = b.Configure.GetCapture().GetFilePath()
			f.recordConfigure(b.Configure)
			enginesOff = b.Configure.GetCapture().GetEngines() != nil && !b.Configure.GetCapture().GetEngines().GetEnabled()
			// before configure, which tests take as the start of a run's frames
			out := b.Configure.GetOutput()
			f.record("configure_output", fmt.Sprintf("channels=%d eq_bands=%d ca_file=%q", out.GetChannels(), len(out.GetEq().GetBands()), b.Configure.GetCaFile()))
			f.record("configure_prompts", b.Configure.GetPromptsDir())
			f.record("configure", capturePath)
			if f.noReady {
				continue
			}
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_Ready{Ready: &audiodv1.Ready{Version: "fake-1"}}})
			f.afterReady(c)
			go f.vendor(c, verdict, enginesOff)
		case *audiodv1.Envelope_VolumeSet:
			detail := strconv.FormatUint(uint64(b.VolumeSet.GetLevel()), 10)
			if b.VolumeSet.GetTransient() {
				detail += " transient"
			}
			f.record("volume_set", detail)
			set := b.VolumeSet
			applied := &audiodv1.VolumeApplied{Level: set.GetLevel(), Ok: !f.volumeFail, Transient: set.GetTransient()}
			switch {
			case applied.Transient && ringing == "":
				applied.Ok, applied.Code = false, "no_ring"
			case !applied.Ok:
				applied.Code = "control"
			}
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_VolumeApplied{VolumeApplied: applied}})
			if f.hang {
				select {}
			}
		case *audiodv1.Envelope_Ping:
			if !f.noPong {
				c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_Pong{Pong: &audiodv1.HealthPong{Seq: b.Ping.GetSeq(), Version: "fake-1"}}})
			}
		case *audiodv1.Envelope_CaptureStart:
			id := b.CaptureStart.GetSessionId()
			// the size of the capture file at CaptureStart, -1 when there is none
			size := int64(-1)
			if st, err := os.Stat(capturePath); err == nil {
				size = st.Size()
			}
			f.record("capture_start", strconv.FormatInt(size, 10))
			if f.noMic {
				continue
			}
			stop := make(chan struct{})
			captures[id] = stop
			go micFrames(c, id, stop, verdict)
		case *audiodv1.Envelope_CaptureStop:
			id := b.CaptureStop.GetSessionId()
			f.record("capture_stop", id)
			if stop, ok := captures[id]; ok {
				close(stop)
				delete(captures, id)
			}
		case *audiodv1.Envelope_StreamStart:
			f.record("stream_start", b.StreamStart.GetStreamId())
		case *audiodv1.Envelope_AudioDown:
			f.record("audio_down", b.AudioDown.GetStreamId())
			if f.crashMidStream && f.run == 1 {
				os.Exit(exitCrash)
			}
		case *audiodv1.Envelope_StreamEnd:
			f.record("stream_end", b.StreamEnd.GetStreamId())
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_StreamDone{StreamDone: &audiodv1.StreamDone{StreamId: b.StreamEnd.GetStreamId(), PlayedMs: 20}}})
		case *audiodv1.Envelope_StreamCancel:
			f.record("stream_cancel", b.StreamCancel.GetStreamId())
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_StreamDone{StreamDone: &audiodv1.StreamDone{StreamId: b.StreamCancel.GetStreamId()}}})
		case *audiodv1.Envelope_DuckSet:
			f.record("duck_set", strconv.FormatBool(b.DuckSet.GetActive()))
		case *audiodv1.Envelope_MuteState:
			f.record("mute_state", strconv.FormatBool(b.MuteState.GetMuted()))
		case *audiodv1.Envelope_MediaSettings:
			s := b.MediaSettings
			f.record("media_settings", fmt.Sprintf("duck=%s eq=%g/%d viz=%t/%s",
				s.GetDuckMode(), s.GetEq().GetPreampDb(), len(s.GetEq().GetBands()), s.GetViz().GetEnabled(), s.GetViz().GetSource()))
		case *audiodv1.Envelope_UrlPlay:
			p := b.UrlPlay
			title := p.GetMetadata().GetTitle()
			f.record("url_play", fmt.Sprintf("%s %d %s title_bytes=%d utf8=%t", p.GetMode(), p.GetStartMs(), p.GetUrl(), len(title), utf8.ValidString(title)))
			f.command(c, p.GetRequestId())
			if f.mediaState && f.mediaCode == "" {
				c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_MediaState{MediaState: &audiodv1.MediaState{
					SessionId: "ms-1", Source: audiodv1.MediaSourceKind_MEDIA_SOURCE_KIND_URL, State: audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING,
					Rate: 1, Metadata: p.GetMetadata(), Actions: []audiodv1.MediaAction{audiodv1.MediaAction_MEDIA_ACTION_PAUSE, audiodv1.MediaAction_MEDIA_ACTION_STOP},
				}}})
			}
		case *audiodv1.Envelope_ExternalSource:
			// the last to play takes the speaker, as audiod's arbitration does, under cadenced's id
			e := b.ExternalSource
			f.record("external_source", fmt.Sprintf("%s %s %s %s", e.GetId(), e.GetOp(), e.GetState(), e.GetAudioRoute()))
			if e.GetOp() == audiodv1.ExternalOp_EXTERNAL_OP_UPDATE && e.GetState() == audiodv1.PlaybackState_PLAYBACK_STATE_PLAYING {
				c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_MediaState{MediaState: &audiodv1.MediaState{
					SessionId: e.GetId(), Source: e.GetKind(), State: e.GetState(), AudioRoute: e.GetAudioRoute(), Rate: 1,
				}}})
			}
		case *audiodv1.Envelope_SessionControl:
			sc := b.SessionControl
			f.record("session_control", fmt.Sprintf("%q duck=%t/%g", sc.GetSessionId(), sc.GetDuck().GetActive(), sc.GetDuck().GetLevel()))
			f.command(c, sc.GetRequestId())
		case *audiodv1.Envelope_TapConfig:
			f.record("tap_config", b.TapConfig.GetPath())
		case *audiodv1.Envelope_MediaCommand:
			f.record("media_command", fmt.Sprintf("%s %d", b.MediaCommand.GetAction(), b.MediaCommand.GetPositionMs()))
			f.command(c, b.MediaCommand.GetRequestId())
		case *audiodv1.Envelope_Tone:
			f.record("tone", fmt.Sprintf("%s prompt=%q lang=%q", b.Tone.GetKind(), b.Tone.GetPromptId(), b.Tone.GetLang()))
		case *audiodv1.Envelope_ToneStart:
			f.record("tone_start", fmt.Sprintf("%s %s", b.ToneStart.GetId(), b.ToneStart.GetKind()))
			ringing = b.ToneStart.GetId()
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_ToneState{ToneState: &audiodv1.ToneState{Id: b.ToneStart.GetId(), Sounding: true}}})
		case *audiodv1.Envelope_ToneStop:
			f.record("tone_stop", b.ToneStop.GetId())
			if f.toneStopCrash && f.run == 1 {
				os.Exit(exitCrash)
			}
			if b.ToneStop.GetId() == ringing {
				ringing = ""
			}
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_ToneState{ToneState: &audiodv1.ToneState{Id: b.ToneStop.GetId(), Code: "stopped"}}})
		default:
			f.soloistEnvelope(c, env, &moved)
		}
	}
}

// soloistEnvelope records what cadenced sends soloist (never the key itself)
// and answers it in --soloist mode
func (f *fake) soloistEnvelope(c *conn, env *audiodv1.Envelope, moved *bool) {
	switch b := env.GetBody().(type) {
	case *audiodv1.Envelope_SoloistConfig:
		s := b.SoloistConfig
		f.record("soloist_config", fmt.Sprintf("enabled=%t key_bytes=%d name=%q dir=%q scratch=%q ca=%q initial=%d",
			s.GetEnabled(), len(s.GetApiKey()), s.GetDeviceName(), s.GetDir(), s.GetScratchDir(), s.GetCaFile(), s.GetInitialVolume()))
		if f.soloist {
			st := &audiodv1.SoloistStatus{State: audiodv1.SoloistState_SOLOIST_STATE_NO_BUILD}
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_SoloistStatus{SoloistStatus: st}})
		}
	case *audiodv1.Envelope_SenderVolumeSet:
		v := b.SenderVolumeSet
		f.record("sender_volume_set", fmt.Sprintf("sender=%s level=%d", v.GetSender(), v.GetLevel()))
		if f.soloist && !*moved {
			*moved = true
			sv := &audiodv1.SenderVolume{Sender: audiodv1.Sender_SENDER_SOLOIST, Level: v.GetLevel() + 5}
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_SenderVolume{SenderVolume: sv}})
		}
	case *audiodv1.Envelope_SoloistProbe:
		p := b.SoloistProbe
		f.record("soloist_probe", filepath.Base(p.GetPath()))
		if f.soloist {
			r := &audiodv1.SoloistProbeResult{RequestId: p.GetRequestId(), Code: "version"}
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_SoloistProbeResult{SoloistProbeResult: r}})
		}
	case *audiodv1.Envelope_AirplayConfig:
		a := b.AirplayConfig
		f.record("airplay_config", fmt.Sprintf("enabled=%t name=%q dir=%q scratch=%q", a.GetEnabled(), a.GetName(), a.GetDir(), a.GetScratchDir()))
		if f.airplay != "" {
			st := &audiodv1.AirplayStatus{State: audiodv1.AirplayState_AIRPLAY_STATE_READY}
			if f.airplay != "ready" {
				st = &audiodv1.AirplayStatus{State: audiodv1.AirplayState_AIRPLAY_STATE_FAILED, Code: f.airplay}
			}
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_AirplayStatus{AirplayStatus: st}})
		}
	case *audiodv1.Envelope_GroupConfig:
		f.groupConfig(c, b.GroupConfig)
	case *audiodv1.Envelope_BluetoothConfig:
		f.record("bluetooth_config", fmt.Sprintf("enabled=%t", b.BluetoothConfig.GetEnabled()))
	case *audiodv1.Envelope_BluetoothStream:
		st := b.BluetoothStream
		f.record("bluetooth_stream", fmt.Sprintf("id=%s rate=%d channels=%d", st.GetStreamId(), st.GetRate(), st.GetChannels()))
		if f.btControl {
			ctl := &audiodv1.BluetoothControl{StreamId: st.GetStreamId(), Action: audiodv1.MediaAction_MEDIA_ACTION_PAUSE}
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_BluetoothControl{BluetoothControl: ctl}})
		}
	case *audiodv1.Envelope_BluetoothStreamEnd:
		f.record("bluetooth_stream_end", "id="+b.BluetoothStreamEnd.GetStreamId())
	case *audiodv1.Envelope_SoloistSwitch:
		f.record("soloist_switch", "")
	case *audiodv1.Envelope_LogsSet:
		f.record("logs_set", fmt.Sprint(b.LogsSet.GetThirdParty()))
	}
}

// groupConfig records a GroupConfig (the key's length, never the key) and answers it in --group mode
func (f *fake) groupConfig(c *conn, g *audiodv1.GroupConfig) {
	f.record("group_config", fmt.Sprintf("enabled=%t group=%q role=%s member=%q host=%q port=%d key_bytes=%d channel=%s latency=%d trim=%d budget=%d bitrate=%d pcm=%t members=%q wait=%d",
		g.GetEnabled(), g.GetGroupId(), g.GetRole(), g.GetMemberId(), g.GetLeaderHost(), g.GetPort(), len(g.GetKey()), g.GetChannel(),
		g.GetLatencyMs(), g.GetTrimMs(), g.GetBudgetUs(), g.GetBitrateKbps(), g.GetCodecPcm(), g.GetMembers(), g.GetWaitMs()))
	if f.group == "" || !g.GetEnabled() {
		return
	}
	st := &audiodv1.GroupStatus{
		GroupId: g.GetGroupId(), Role: g.GetRole(), Synced: true, ClockErrorUs: 42, QueueMs: 1200, LatencyMs: g.GetLatencyMs(),
		State: audiodv1.GroupState(audiodv1.GroupState_value["GROUP_STATE_"+strings.ToUpper(f.group)]),
	}
	if g.GetRole() == audiodv1.GroupRole_GROUP_ROLE_LEADER {
		st.Members = []*audiodv1.GroupMemberStatus{{MemberId: "member-1", State: st.State, Connected: true, Synced: true, ClockErrorUs: 17}}
	}
	c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_GroupStatus{GroupStatus: st}})
}

// command answers a UrlPlay or MediaCommand, or crashes on it
func (f *fake) command(c *conn, id string) {
	if f.crashOnCommand && f.run == 1 {
		os.Exit(exitCrash)
	}
	r := &audiodv1.CommandResult{RequestId: id, Ok: f.mediaCode == "", Code: f.mediaCode}
	c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_CommandResult{CommandResult: r}})
}

func (f *fake) afterReady(c *conn) {
	if f.stderr {
		for _, text := range []string{"SECRET_IPC", `{"level":"INFO","msg":"fake_ipc_event","n":1}`} {
			line := &audiodv1.LogLine{Source: "audiod", Level: audiodv1.LogLevel_LOG_LEVEL_INFO, Text: text}
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_LogLine{LogLine: line}})
		}
		// children's lines, which never reach the local log, whatever they look like
		for _, line := range []*audiodv1.LogLine{
			{Source: "soloist", Level: audiodv1.LogLevel_LOG_LEVEL_WARN, Text: "SECRET_CHILD track"},
			{Source: "mystery", Level: audiodv1.LogLevel_LOG_LEVEL_ERROR, Text: `{"level":"ERROR","msg":"SECRET_JSON_CHILD"}`, Truncated: true},
		} {
			c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_LogLine{LogLine: line}})
		}
	}
	if f.mediaState {
		idle := &audiodv1.MediaState{State: audiodv1.PlaybackState_PLAYBACK_STATE_IDLE}
		c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_MediaState{MediaState: idle}})
		bands := make([]byte, 16)
		bands[0], bands[15] = 7, 200
		c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_VizFrame{VizFrame: &audiodv1.VizFrame{Bands: bands}}})
	}
	if f.run <= f.crashRuns {
		os.Exit(exitCrash)
	}
	if !f.exitAfterReady {
		return
	}
	if f.run != f.holdRun {
		os.Exit(exitCrash)
	}
	go func() {
		time.Sleep(time.Duration(f.holdMS) * time.Millisecond)
		os.Exit(exitCrash)
	}()
}

// vendor sends the --vendor-ms verdict, then the --wake events, and closes verdict; without --vendor-ms it sends no
// VendorStatus, as for a file capture. engines the Configure turned off answer as audiod's do: off, disabled
func (f *fake) vendor(c *conn, verdict chan struct{}, enginesOff bool) {
	if f.vendorMS > 0 {
		time.Sleep(time.Duration(f.vendorMS) * time.Millisecond)
		st := &audiodv1.VendorStatus{Engines: true, Code: f.vendorCode}
		if enginesOff {
			st = &audiodv1.VendorStatus{Engines: false, Code: "disabled"}
		}
		c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_VendorStatus{VendorStatus: st}})
	}
	close(verdict)
	if f.wake {
		c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_WakeEvent{WakeEvent: &audiodv1.WakeEvent{Engine: "stock", Score: 1, AtMs: time.Now().UnixMilli()}}})
		c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_CommandWord{CommandWord: &audiodv1.CommandWord{Id: 2, Score: 1, AtMs: time.Now().UnixMilli()}}})
	}
}

// micFrames sends one MicOpus per 20 ms, the first once the vendor verdict went out, until stop closes
func micFrames(c *conn, id string, stop, verdict <-chan struct{}) {
	select {
	case <-verdict:
	case <-stop:
		return
	}
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for seq := uint32(0); ; seq++ {
		c.send(&audiodv1.Envelope{Body: &audiodv1.Envelope_MicOpus{MicOpus: &audiodv1.MicOpus{SessionId: id, Seq: seq, Opus: []byte{0xf8, 0xff, 0xfe}}}})
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// recordConfigure keeps the capture, engine and playback fields a board sends, before configure_output
func (f *fake) recordConfigure(cfg *audiodv1.Configure) {
	cp, out := cfg.GetCapture(), cfg.GetOutput()
	l := cp.GetLayout()
	f.record("configure_capture", fmt.Sprintf("backend=%s device=%q rate=%d channels=%d period=%d buffer=%d layout=%d/%v/%v/%d",
		cp.GetBackend(), cp.GetDevice(), cp.GetSampleRate(), cp.GetChannels(), cp.GetPeriodFrames(), cp.GetBufferFrames(),
		l.GetChannels(), l.GetMicChannels(), l.GetReferenceChannels(), l.GetSampleRate()))
	if e := cp.GetEngines(); e != nil {
		f.record("configure_engines", fmt.Sprintf("enabled=%t roots=%q library=%q activation=%q command=%q preset=%q words=%q",
			e.GetEnabled(), e.GetStockRoots(), e.GetLibrary(), e.GetActivationModel(), e.GetCommandModel(), e.GetVqePreset(), e.GetCommandWords()))
	}
	if k := cp.GetKeyword(); k != nil {
		f.record("configure_keyword", fmt.Sprintf("kind=%q model=%q sha256=%q pos=%g sp=%g rearm=%d consecutive=%d",
			k.GetKind(), k.GetModelPath(), k.GetModelSha256(), k.GetPosThreshold(), k.GetSpThreshold(), k.GetRearmFrames(), k.GetConsecutiveCalls()))
	}
	f.record("configure_playback", fmt.Sprintf("backend=%s device=%q mixer=%q period=%d buffer=%d",
		out.GetBackend(), out.GetDevice(), out.GetMixerControl(), out.GetPeriodFrames(), out.GetBufferFrames()))
}
