package commands_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/santhosh-tekuri/jsonschema/v6"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/api"
	"git.shino.boo/devilreef/cadence/server/internal/api/apitest"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
	"git.shino.boo/devilreef/cadence/server/internal/gateway"
	"git.shino.boo/devilreef/cadence/server/internal/jsonschemax"
)

const (
	devID      = "dev-1"
	seekPath   = "/api/v1/devices/dev-1/capabilities/media.player/commands/seek"
	pausePath  = "/api/v1/devices/dev-1/capabilities/media.player/commands/pause"
	playPath   = "/api/v1/devices/dev-1/capabilities/media.url/commands/play"
	noArgs     = `{"type":"object","additionalProperties":false}`
	seekSchema = `{"type":"object","properties":{"positionMs":{"type":"integer","minimum":0}},"required":["positionMs"],"additionalProperties":false}`
	playSchema = `{"type":"object","properties":{"url":{"type":"string","pattern":"^https?://."},"blob":{"type":"string"}},"required":["url"],"additionalProperties":false}`
)

// fakeLookup reports one device, as devices.Service would.
type fakeLookup map[string]map[string]string

func (f fakeLookup) Command(_ context.Context, dev, capability, name string) (string, *jsonschema.Schema, error) {
	if dev == "gone" {
		// the caller went away during the lookup
		return "", nil, context.Canceled
	}
	if dev == "broken" {
		return "", nil, errors.New("password authentication failed: secret-db-text")
	}
	if dev != devID {
		return "", nil, devices.ErrNotFound
	}
	cmds, ok := f[capability]
	if !ok {
		return "", nil, devices.ErrUnknownCapability
	}
	src, ok := cmds[name]
	if !ok {
		return "", nil, devices.ErrUnknownCommand
	}
	sch, err := jsonschemax.Compile(src)
	return dev, sch, err
}

var lookup = fakeLookup{
	"media.player": {"pause": noArgs, "seek": seekSchema, "next": noArgs, "previous": noArgs},
	"media.url":    {"play": playSchema},
	"audio.output": {"mute": noArgs},
	"alarms":       {"set_alarm": noArgs},
}

// fakeConn is a device link: answer, when set, makes the device's reply, which
// arrives on another goroutine after Send returned, as the gateway's reader
// would deliver it.
type fakeConn struct {
	svc *commands.Service

	mu      sync.Mutex
	got     []*cadencev2.CapabilityCommand
	answer  func(*cadencev2.CapabilityCommand) *cadencev2.CommandResult
	sendErr error
	sent    chan *cadencev2.CapabilityCommand
}

func (f *fakeConn) DeviceID() string { return devID }

func (f *fakeConn) Send(env *cadencev2.Envelope) error {
	f.mu.Lock()
	if f.sendErr != nil {
		defer f.mu.Unlock()
		return f.sendErr
	}
	cmd := env.GetCapabilityCommand()
	f.got = append(f.got, cmd)
	answer := f.answer
	f.mu.Unlock()
	if f.sent != nil {
		f.sent <- cmd
	}
	if answer != nil {
		if r := answer(cmd); r != nil {
			go f.svc.Handle(f, result(r))
		}
	}
	return nil
}

func (f *fakeConn) commands() []*cadencev2.CapabilityCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*cadencev2.CapabilityCommand(nil), f.got...)
}

func result(r *cadencev2.CommandResult) *cadencev2.Envelope {
	return &cadencev2.Envelope{Body: &cadencev2.Envelope_CommandResult{CommandResult: r}}
}

// lockedBuffer lets a test read the log while handlers still write to it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type env struct {
	svc    *commands.Service
	app    *fiber.App
	conn   *fakeConn
	unlink context.CancelFunc // ends the link, as a close or a replacement does
	logs   *lockedBuffer
}

// newEnv links the device; its fake answers nothing until a test sets answer.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := unlinked(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.svc.Linked(ctx, e.conn)
	e.unlink = cancel
	return e
}

func unlinked(t *testing.T) *env {
	t.Helper()
	logs := &lockedBuffer{}
	svc := commands.New(lookup, slog.New(slog.NewTextHandler(logs, nil)))
	app := api.NewApp(api.Options{
		DB: noPing{}, Version: "t",
		VerifyToken: apitest.Verify,
		Admin:       []func(huma.API){func(a huma.API) { commands.RegisterAdmin(a, svc) }},
	})
	return &env{svc: svc, app: app, conn: &fakeConn{svc: svc}, unlink: func() {}, logs: logs}
}

func (e *env) answer(fn func(*cadencev2.CapabilityCommand) *cadencev2.CommandResult) {
	e.conn.mu.Lock()
	defer e.conn.mu.Unlock()
	e.conn.answer = fn
}

func ok(json string) func(*cadencev2.CapabilityCommand) *cadencev2.CommandResult {
	return func(c *cadencev2.CapabilityCommand) *cadencev2.CommandResult {
		return &cadencev2.CommandResult{RequestId: c.GetRequestId(), Json: json}
	}
}

func failed(code, json string) func(*cadencev2.CapabilityCommand) *cadencev2.CommandResult {
	return func(c *cadencev2.CapabilityCommand) *cadencev2.CommandResult {
		return &cadencev2.CommandResult{RequestId: c.GetRequestId(), Code: code, Json: json}
	}
}

type noPing struct{}

func (noPing) Ping(context.Context) error { return nil }

func (e *env) post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	return e.request(t, http.MethodPost, path, body)
}

func (e *env) get(t *testing.T, path string) (int, string) {
	t.Helper()
	return e.request(t, http.MethodGet, path, "")
}

func (e *env) request(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer x")
	resp, err := e.app.Test(req, fiber.TestConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestCommandRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.answer(ok(`{"positionMs":5}`))
	code, body := e.post(t, seekPath, `{ "positionMs": 5 }`)
	if code != 200 || body != `{"result":{"positionMs":5}}` {
		t.Fatalf("%d %s", code, body)
	}
	e.answer(ok(""))
	code, body = e.post(t, pausePath, "")
	if code != 200 || body != `{"result":null}` {
		t.Fatalf("%d %s", code, body)
	}
	got := e.conn.commands()
	// the device's result passes as it came, compacted only: not re-encoded
	e.answer(ok(`{"title": "Rock & <Roll>"}`))
	if code, body = e.post(t, seekPath, `{"positionMs":5}`); code != 200 || body != `{"result":{"title":"Rock & <Roll>"}}` {
		t.Fatalf("%d %s", code, body)
	}
	if len(got) != 2 {
		t.Fatalf("sent %d", len(got))
	}
	seek, pause := got[0], got[1]
	if seek.GetCapability() != "media.player" || seek.GetName() != "seek" || seek.GetJson() != `{"positionMs":5}` {
		t.Fatalf("seek %v", seek)
	}
	// the device answers an empty json invalid, so no args go as {}
	if pause.GetName() != "pause" || pause.GetJson() != "{}" {
		t.Fatalf("pause %v", pause)
	}
	for _, c := range got {
		if n := len(c.GetRequestId()); n == 0 || n > 63 {
			t.Fatalf("request id of %d bytes", n)
		}
	}
	if seek.GetRequestId() == pause.GetRequestId() {
		t.Fatal("request id reused")
	}
	if !strings.Contains(e.logs.String(), "msg=command device_id=dev-1 capability=media.player name=seek code=ok ms=") {
		t.Fatalf("log: %s", e.logs)
	}
}

func TestCommandDeviceCode(t *testing.T) {
	e := newEnv(t)
	// the device's code is the detail when it is a token (ruling M7a-R1); free text is
	// no detail, and the log calls it failed
	cases := []struct{ code, body, logged string }{
		{"no_session", `{"error":"command_failed","detail":"no_session"}`, "no_session"},
		{"failed", `{"error":"command_failed","detail":"failed"}`, "failed"},
		{"Not A Token! device text", `{"error":"command_failed"}`, "failed"},
		{"a" + strings.Repeat("b", 63), `{"error":"command_failed"}`, "failed"},
	}
	for _, c := range cases {
		e.answer(failed(c.code, `{"detail":"device text"}`))
		code, body := e.post(t, pausePath, `{}`)
		if code != 409 || body != c.body {
			t.Fatalf("%q: %d %s", c.code, code, body)
		}
		if !strings.Contains(e.logs.String(), "code="+c.logged+" ") {
			t.Fatalf("%q: log: %s", c.code, e.logs)
		}
	}
	if strings.Contains(e.logs.String(), "device text") {
		t.Fatalf("log: %s", e.logs)
	}
}

func TestCommandTimeout(t *testing.T) {
	e := newEnv(t)
	commands.SetTimeout(e.svc, 50*time.Millisecond)
	code, body := e.post(t, pausePath, `{}`)
	if code != 504 || body != `{"error":"timeout"}` {
		t.Fatalf("%d %s", code, body)
	}
	// a result past the timeout finds no one waiting
	late := e.conn.commands()[0].GetRequestId()
	if !e.svc.Handle(e.conn, result(&cadencev2.CommandResult{RequestId: late})) {
		t.Fatal("result not consumed")
	}
	if !strings.Contains(e.logs.String(), "command_result_dropped") {
		t.Fatalf("log: %s", e.logs)
	}
}

func TestCommandOffline(t *testing.T) {
	cases := map[string]func(*testing.T) *env{
		"never linked": unlinked,
		"closing": func(t *testing.T) *env {
			e := newEnv(t)
			e.unlink()
			return e
		},
		"closed": func(t *testing.T) *env {
			e := newEnv(t)
			e.unlink()
			e.svc.Closed(devID)
			return e
		},
	}
	for name, setup := range cases {
		e := setup(t)
		e.answer(ok(""))
		code, body := e.post(t, pausePath, `{}`)
		if code != 409 || body != `{"error":"device_offline"}` {
			t.Fatalf("%s: %d %s", name, code, body)
		}
		if n := len(e.conn.commands()); n != 0 {
			t.Fatalf("%s: sent %d to an offline device", name, n)
		}
	}
}

func TestCommandSendRefused(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		err        error
		code       int
		wantedBody string
	}{
		{gateway.ErrOffline, 409, `{"error":"device_offline"}`},
		{gateway.ErrBackpressure, 429, `{"error":"busy"}`},
	}
	for _, c := range cases {
		e.conn.mu.Lock()
		e.conn.sendErr = c.err
		e.conn.mu.Unlock()
		code, body := e.post(t, pausePath, `{}`)
		if code != c.code || body != c.wantedBody {
			t.Fatalf("%v: %d %s", c.err, code, body)
		}
	}
}

func TestCommandDisconnectWhileWaiting(t *testing.T) {
	e := newEnv(t)
	e.answer(func(*cadencev2.CapabilityCommand) *cadencev2.CommandResult {
		go func() {
			e.unlink()
			e.svc.Closed(devID)
		}()
		return nil
	})
	start := time.Now()
	code, body := e.post(t, pausePath, `{}`)
	if code != 409 || body != `{"error":"device_offline"}` || time.Since(start) > 5*time.Second {
		t.Fatalf("%d %s", code, body)
	}
}

func TestCommandInvalidArguments(t *testing.T) {
	e := newEnv(t)
	e.answer(ok(""))
	cases := []struct{ path, body, want string }{
		{playPath, `{"url":"ftp://secret.example/x"}`, `{"error":"invalid_arguments","path":"/url"}`},
		{seekPath, `{"positionMs":-7}`, `{"error":"invalid_arguments","path":"/positionMs"}`},
		{playPath, `["https://secret.example/x"]`, `{"error":"invalid_arguments","path":""}`},
		{playPath, `{"url":"https://secret.example/x"`, `{"error":"invalid_arguments","path":""}`},
		{playPath, `{"url":"https://secret.example/x"} {}`, `{"error":"invalid_arguments","path":""}`},
	}
	for _, c := range cases {
		code, body := e.post(t, c.path, c.body)
		if code != 422 || body != c.want {
			t.Fatalf("%s: %d %s", c.body, code, body)
		}
	}
	if n := len(e.conn.commands()); n != 0 {
		t.Fatalf("sent %d invalid commands", n)
	}
	if strings.Contains(e.logs.String(), "secret") || strings.Contains(e.logs.String(), "-7") {
		t.Fatalf("log: %s", e.logs)
	}
}

func TestCommandArgumentsTooLarge(t *testing.T) {
	e := newEnv(t)
	blob := strings.Repeat("x", 17*1024)
	// args past one frame are refused before they are parsed, valid or not
	for _, body := range []string{
		`{"url":"https://x.example/","blob":"` + blob + `"}`,
		`{"url":"ftp://x.example/","blob":"` + blob + `"}`,
		`{"blob":"` + blob,
	} {
		code, got := e.post(t, playPath, body)
		if code != 413 || got != `{"error":"arguments_too_large"}` {
			t.Fatalf("%d %.80s", code, got)
		}
	}
	// 16380 bytes of args fit the limit, the frame around them does not
	code, got := e.post(t, playPath, `{"url":"https://x.example/","blob":"`+strings.Repeat("x", 16342)+`"}`)
	if code != 413 || got != `{"error":"arguments_too_large"}` {
		t.Fatalf("%d %.80s", code, got)
	}
	if n := len(e.conn.commands()); n != 0 {
		t.Fatalf("sent %d", n)
	}
}

func TestCommandUnknown(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ path, want string }{
		{"/api/v1/devices/nope/capabilities/media.player/commands/pause", `{"error":"not_found"}`},
		{"/api/v1/devices/dev-1/capabilities/nope/commands/pause", `{"error":"unknown_capability"}`},
		{"/api/v1/devices/dev-1/capabilities/media.player/commands/nope", `{"error":"unknown_command"}`},
	}
	for _, c := range cases {
		code, body := e.post(t, c.path, `{}`)
		if code != 404 || body != c.want {
			t.Fatalf("%s: %d %s", c.path, code, body)
		}
	}
}

func TestCommandBusyPastEight(t *testing.T) {
	e := newEnv(t)
	e.conn.sent = make(chan *cadencev2.CapabilityCommand, 16)
	type outcome struct {
		res json.RawMessage
		err error
	}
	done := make(chan outcome, 8)
	for range 8 {
		go func() {
			res, err := e.svc.Send(context.Background(), devID, "media.player", "pause", nil)
			done <- outcome{res, err}
		}()
	}
	held := make([]*cadencev2.CapabilityCommand, 0, 8)
	for range 8 {
		select {
		case c := <-e.conn.sent:
			held = append(held, c)
		case <-time.After(5 * time.Second):
			t.Fatal("held commands not sent")
		}
	}
	code, body := e.post(t, pausePath, `{}`)
	if code != 429 || body != `{"error":"busy"}` {
		t.Fatalf("%d %s", code, body)
	}
	for _, c := range held {
		e.svc.Handle(e.conn, result(&cadencev2.CommandResult{RequestId: c.GetRequestId(), Json: `{"n":1}`}))
	}
	for range 8 {
		o := <-done
		if o.err != nil || string(o.res) != `{"n":1}` {
			t.Fatalf("held command: %s %v", o.res, o.err)
		}
	}
	// the slots came back
	e.answer(ok(""))
	if code, body := e.post(t, pausePath, `{}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
}

func TestStrayResultDropped(t *testing.T) {
	e := newEnv(t)
	e.conn.sent = make(chan *cadencev2.CapabilityCommand, 1)
	type outcome struct {
		res json.RawMessage
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := e.svc.Send(context.Background(), devID, "media.player", "pause", []byte(`{}`))
		done <- outcome{res, err}
	}()
	var cmd *cadencev2.CapabilityCommand
	select {
	case cmd = <-e.conn.sent:
	case <-time.After(5 * time.Second):
		t.Fatal("command not sent")
	}
	// a result for another request, and this request's id from another link
	other := &fakeConn{svc: e.svc}
	e.svc.Handle(e.conn, result(&cadencev2.CommandResult{RequestId: "nope", Json: `{"stray":1}`}))
	e.svc.Handle(other, result(&cadencev2.CommandResult{RequestId: cmd.GetRequestId(), Json: `{"stray":2}`}))
	select {
	case o := <-done:
		t.Fatalf("a stray result answered: %s %v", o.res, o.err)
	case <-time.After(50 * time.Millisecond):
	}
	e.svc.Handle(e.conn, result(&cadencev2.CommandResult{RequestId: cmd.GetRequestId(), Json: `{"mine":true}`}))
	if o := <-done; o.err != nil || string(o.res) != `{"mine":true}` {
		t.Fatalf("%s %v", o.res, o.err)
	}
	if n := strings.Count(e.logs.String(), "command_result_dropped"); n != 2 {
		t.Fatalf("dropped %d: %s", n, e.logs)
	}
}

func TestHandleIgnoresOtherMessages(t *testing.T) {
	e := newEnv(t)
	other := &cadencev2.Envelope{Body: &cadencev2.Envelope_MediaSessionState{MediaSessionState: &cadencev2.MediaSessionState{}}}
	if e.svc.Handle(e.conn, other) {
		t.Fatal("consumed a media state")
	}
}

// a lookup failure can carry driver text: neither the body nor the log has it
func TestCommandFailureIsInternalWithoutItsCause(t *testing.T) {
	e := newEnv(t)
	code, body := e.post(t, "/api/v1/devices/broken/capabilities/media.player/commands/pause", `{}`)
	if code != 500 || body != `{"error":"internal"}` {
		t.Fatalf("%d %s", code, body)
	}
	if logs := e.logs.String(); !strings.Contains(logs, "msg=command_error code=internal") || strings.Contains(logs, "secret") {
		t.Fatalf("log: %s", logs)
	}
}

// a caller that goes away is no server error
func TestCommandCallerGone(t *testing.T) {
	e := newEnv(t)
	code, body := e.post(t, "/api/v1/devices/gone/capabilities/media.player/commands/pause", `{}`)
	if code != 499 || body != `{"error":"canceled"}` {
		t.Fatalf("%d %s", code, body)
	}
	e.conn.sent = make(chan *cadencev2.CapabilityCommand, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-e.conn.sent
		cancel()
	}()
	if _, err := e.svc.Send(ctx, devID, "media.player", "pause", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "level=INFO msg=command device_id=dev-1 capability=media.player name=pause code=canceled") ||
		strings.Contains(logs, "level=ERROR") {
		t.Fatalf("log: %s", logs)
	}
}

// an ha token sends a device's commands: play, pause, volume and the rest a
// capability offers (M7a-D6), next and previous among them (M8-D5)
func TestHATokenSendsCommands(t *testing.T) {
	e := newEnv(t)
	e.answer(ok(""))
	for _, name := range []string{"pause", "next", "previous"} {
		path := "/api/v1/devices/dev-1/capabilities/media.player/commands/" + name
		if code, body := apitest.Call(t, e.app, apitest.HABearer, http.MethodPost, path, ""); code != 200 || body != `{"result":null}` {
			t.Fatalf("%s: %d %s", name, code, body)
		}
	}
	got := e.conn.commands()
	if len(got) != 3 || got[0].GetName() != "pause" || got[1].GetName() != "next" || got[2].GetName() != "previous" {
		t.Fatalf("sent %v", got)
	}
	// a session whose actions list no skip refuses it on the device (M8-R2)
	e.answer(failed("unsupported", ""))
	path := "/api/v1/devices/dev-1/capabilities/media.player/commands/next"
	if code, body := apitest.Call(t, e.app, apitest.HABearer, http.MethodPost, path, ""); code != 409 ||
		body != `{"error":"command_failed","detail":"unsupported"}` {
		t.Fatalf("refused next: %d %s", code, body)
	}
}

// an ha token sends only the commands its allow-list names (Ruling M7a-R4): an
// alarm, a command or a capability the list leaves out is refused before
// anything is sent; admin sends them
func TestHATokenCommandAllowList(t *testing.T) {
	e := newEnv(t)
	e.answer(ok(""))
	const alarmPath = "/api/v1/devices/dev-1/capabilities/alarms/commands/set_alarm"
	for _, path := range []string{alarmPath, "/api/v1/devices/dev-1/capabilities/audio.output/commands/mute",
		"/api/v1/devices/dev-1/capabilities/bogus/commands/pause"} {
		if code, body := apitest.Call(t, e.app, apitest.HABearer, http.MethodPost, path, ""); code != 403 || body != `{"error":"forbidden"}` {
			t.Fatalf("%s: %d %s", path, code, body)
		}
	}
	if got := e.conn.commands(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
	if n := strings.Count(e.logs.String(), "msg=api_forbidden op=send-device-command scope=admin\n"); n != 3 {
		t.Fatalf("log %s", e.logs)
	}
	if code, body := e.post(t, alarmPath, ""); code != 200 || body != `{"result":null}` {
		t.Fatalf("admin: %d %s", code, body)
	}
	if code, body := apitest.Call(t, e.app, apitest.HABearer, http.MethodPost, seekPath, `{"positionMs":5}`); code != 200 {
		t.Fatalf("ha seek: %d %s", code, body)
	}
	if got := e.conn.commands(); len(got) != 2 || got[0].GetCapability() != "alarms" || got[1].GetName() != "seek" {
		t.Fatalf("sent %v", got)
	}
}

func TestCommandWithWaitOutlastsTheUsualTimeout(t *testing.T) {
	e := newEnv(t)
	commands.SetTimeout(e.svc, 20*time.Millisecond)
	e.conn.sent = make(chan *cadencev2.CapabilityCommand, 1)
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.Deliver(commands.WithWait(context.Background(), 5*time.Second), devID, "media.player", "pause", []byte(`{}`))
		done <- err
	}()
	cmd := <-e.conn.sent
	time.Sleep(100 * time.Millisecond)
	e.svc.Handle(e.conn, result(&cadencev2.CommandResult{RequestId: cmd.GetRequestId(), Json: `{}`}))
	if err := <-done; err != nil {
		t.Fatalf("err %v", err)
	}
}
