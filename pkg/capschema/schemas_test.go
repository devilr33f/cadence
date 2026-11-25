package capschema_test

import (
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

func compile(t *testing.T, src string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("s.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("s.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func check(t *testing.T, src string, cases map[string]bool) {
	t.Helper()
	s := compile(t, src)
	for doc, valid := range cases {
		v, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Validate(v) == nil; got != valid {
			t.Errorf("%s: valid = %v, want %v", doc, got, valid)
		}
	}
}

func TestEveryCommandCompiles(t *testing.T) {
	for _, cmds := range []map[string]string{
		capschema.DisplayScreenCommands, capschema.MediaVideoCommands, capschema.InputRemoteCommands,
	} {
		for name, src := range cmds {
			t.Run(name, func(t *testing.T) { compile(t, src) })
		}
	}
}

func TestDisplayScreenConfig(t *testing.T) {
	check(t, capschema.DisplayScreenConfig, map[string]bool{
		`{}`: true,
		`{"theme":"afterglow","overscan":2.5,"reducedMotion":true,"visualizer":"milkdrop","autoSkipOpenings":true,"cec":false}`:                                                                                                     true,
		`{"ambient":{"source":"aerial","categories":["space","cities"],"idleAfterS":600,"sleepAfterS":0,"showClock":true,"showWeather":true,"showCaptions":false,"aerialQuality":"1080","nightDim":{"from":"23:00","to":"07:30"}}}`: true,
		`{"subtitles":{"size":"m","style":"box"}}`:               true,
		`{"language":"ru"}`:                                      true,
		`{"language":"de"}`:                                      false,
		`{"overscan":3}`:                                         false,
		`{"ambient":{"source":"netflix"}}`:                       false,
		`{"ambient":{"categories":["space","space"]}}`:           false,
		`{"ambient":{"idleAfterS":5}}`:                           false,
		`{"ambient":{"pausedAfterS":0}}`:                         true,
		`{"ambient":{"pausedAfterS":900}}`:                       true,
		`{"ambient":{"pausedAfterS":-1}}`:                        false,
		`{"ambient":{"nightDim":{"from":"24:00","to":"07:00"}}}`: false,
		`{"ambient":{"nightDim":{"from":"23:00"}}}`:              false,
		`{"visualizer":"winamp"}`:                                false,
		`{"other":1}`:                                            false,
		// v2's movies stay out until a screen plays them
		`{"bootMovie":{"ref":"m:1","sound":"off","fit":"fit"}}`:    false,
		`{"suspendMovie":{"ref":"m:1","sound":"off","fit":"fit"}}`: false,
		`{"wakeMovie":{"ref":"m:1","sound":"off","fit":"fit"}}`:    false,
	})
}

func TestDisplayScreenState(t *testing.T) {
	check(t, capschema.DisplayScreenState, map[string]bool{
		`{"power":"on","screen":true}`: true,
		`{"power":"ambient","route":"player","drawerOpen":true,"width":1920,"height":1080,"refreshMhz":60000,"hdr":["hdr10"],"screen":true,"maxHeight":2160,"codecs":["h264","hevc"],"cardVersions":[1],"cardFeatures":["card","post.quote"],"mic":{"kind":"speaker","deviceId":"d1","offline":false}}`: true,
		`{"power":"on","screen":true,"mic":{"kind":"none"}}`: true,
		`{"power":"on","screen":true,"route":"boot"}`:        false,
		`{"power":"off","screen":true}`:                      false,
		`{"power":"on"}`:                                     false,
		`{"power":"on","screen":true,"mic":{}}`:              false,
		`{"power":"on","screen":true,"pageRef":"t:1"}`:       false,
	})
}

func TestDisplayScreenCommands(t *testing.T) {
	check(t, capschema.DisplayScreenCommands["toast"], map[string]bool{
		`{"text":"hi"}`: true,
		`{"text":"hi","kind":"warning","icon":"hand","durationMs":6000}`: true,
		`{"text":""}`:                   false,
		`{"text":"hi","durationMs":10}`: false,
		`{"text":"hi","icon":"A B"}`:    false,
		`{}`:                            false,
		`{"text":"hi","action":{"id":"open","label":"Открыть","kind":"open","route":"task","ref":"ab12"}}`: true,
		`{"text":"hi","action":{"id":"retry","label":"Again","kind":"rpc"}}`:                               true,
		`{"text":"hi","action":{"id":"open","label":"Open","kind":"open"}}`:                                false,
		`{"text":"hi","action":{"id":"open","label":"","kind":"rpc"}}`:                                     false,
		`{"text":"hi","action":{"id":"open","label":"Open","kind":"url"}}`:                                 false,
		`{"text":"hi","action":{"id":"a b","label":"Open","kind":"rpc"}}`:                                  false,
	})
	check(t, capschema.DisplayScreenCommands["show"], map[string]bool{
		`{"route":"home"}`: true,
		`{"route":"episodes","ref":"t:1","episode":"5.5"}`: true,
		`{"route":"back"}`:           true,
		`{"route":"resolving"}`:      false,
		`{"route":"title","ref":""}`: false,
		`{}`:                         false,
	})
	check(t, capschema.DisplayScreenCommands["wake"], map[string]bool{`{}`: true, `{"x":1}`: false})
}

func TestMediaVideo(t *testing.T) {
	check(t, capschema.MediaVideoConfig, map[string]bool{
		`{}`: true,
		`{"hwdec":"mediacodec","audioLanguage":"ru","subtitleLanguage":"en-US","bufferS":30}`: true,
		`{"audioLanguage":"Russian"}`: false,
		`{"bufferS":0}`:               false,
	})
	check(t, capschema.MediaVideoState, map[string]bool{
		`{"tracks":[]}`: true,
		`{"sessionId":"s","tracks":[{"id":"a1","kind":"audio","language":"ru","label":"AniLibria","selected":true}]}`: true,
		`{"tracks":[{"id":"a1","kind":"audio"}]}`: false,
		`{}`: false,
	})
	check(t, capschema.MediaVideoCommands["play"], map[string]bool{
		`{"url":"http://192.168.1.10/play/1"}`: true,
		`{"url":"https://a.example/v.m3u8","audioUrl":"https://a.example/a.m4a","headers":{"Referer":"https://x.example/"},"subtitles":[{"url":"https://a.example/s.vtt","language":"ru","format":"vtt","default":true}],"startMs":1000,"live":false,"metadata":{"title":"t","series":"s","episode":"5.5","itemRef":"e:1"}}`: true,
		`{"url":"file:///etc/passwd"}`:                    false,
		`{"url":"http://a","headers":{"Bad Header":"x"}}`: false,
		`{"url":"http://a","metadata":{"kind":"x"}}`:      false,
		`{"url":"http://a","renditions":[{"name":"1080p","height":1080,"fps":30,"codecs":["vp9","opus"]},{"name":"720p60","height":720,"fps":60,"codecs":["h264"],"selected":true}]}`: true,
		`{"url":"http://a","renditions":[{"name":"hd","height":720}]}`:                                            false,
		`{"url":"http://a","renditions":[{"name":"720p"}]}`:                                                       false,
		`{"url":"http://a","renditions":[{"name":"720p","height":720,"url":"x"}]}`:                                false,
		`{"url":"http://a","skips":[{"kind":"opening","startMs":0,"endMs":90000}],"actions":["next","previous"]}`: true,
		`{"url":"http://a","skips":[{"kind":"intro","startMs":0,"endMs":90000}]}`:                                 false,
		`{"url":"http://a","skips":[{"kind":"opening","startMs":0}]}`:                                             false,
		`{"url":"http://a","actions":["pause"]}`:                                                                  false,
		`{}`:                                                                                                      false,
	})
	// what the screen shows of a play: the channel or artist, the provider's token, name and source, the episode's
	// own name
	check(t, capschema.MediaVideoCommands["play"], map[string]bool{
		`{"url":"http://a","metadata":{"title":"t","artist":"a","channel":"c","provider":"youtube","providerName":"YouTube"}}`:                                                                     true,
		`{"url":"http://a","metadata":{"provider":"YouTube Music"}}`:                                                                                                                               false,
		`{"url":"http://a","metadata":{"title":"t","series":"s","episode":"7","episodeName":"The Guild Advisor","provider":"torrplayer","providerName":"torrplayer","providerSource":"anime365"}}`: true,
		`{"url":"http://a","metadata":{"episodeName":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}`:        false,
	})
	// a follower's play (multiroom P4): the group its picture follows, and the stop of its own session
	check(t, capschema.MediaVideoCommands["play"], map[string]bool{
		`{"url":"http://a","follow":{"groupId":"0b6f3a52-6a1e-4d2b-9c51-3c1d2a4e5f60"}}`: true,
		`{"url":"http://a","follow":{"groupId":"g-1"}}`:                                  false,
		`{"url":"http://a","follow":{}}`:                                                 false,
	})
	check(t, capschema.MediaVideoState, map[string]bool{
		`{"sessionId":"video-2","tracks":[],"follow":{"groupId":"0b6f3a52-6a1e-4d2b-9c51-3c1d2a4e5f60"}}`: true,
		`{"tracks":[],"follow":{"groupId":"0b6f3a52-6a1e-4d2b-9c51-3c1d2a4e5f60","leader":"x"}}`:          false,
	})
	check(t, capschema.MediaVideoCommands["stop"], map[string]bool{
		`{"sessionId":"video-2"}`: true,
		`{"sessionId":""}`:        false,
		`{}`:                      false,
	})
	check(t, capschema.MediaVideoCommands["select_track"], map[string]bool{
		`{"kind":"subtitle","id":""}`: true,
		`{"kind":"audio","id":"a2"}`:  true,
		`{"id":"a2"}`:                 false,
	})
}

func TestMediaMpv(t *testing.T) {
	// every key at once, and each range's edges
	check(t, capschema.MediaMpvConfig, map[string]bool{
		`{}`: true,
		`{"profile":"high-quality","hwdec":"mediacodec","cacheEnabled":true,"demuxerMaxBytes":1048576,` +
			`"demuxerMaxBackBytes":2147483647,"cacheSecs":600,"readaheadS":1,"networkTimeoutS":60,` +
			`"streamBufferBytes":4096,"userAgent":"Cadence/1.0 (Linux; Android 9)","videoSync":"display-resample",` +
			`"interpolation":true,"tscale":"oversample","scale":"spline36","cscale":"spline36","dscale":"mitchell",` +
			`"deband":true,"sharpen":1.5,"audioLanguages":"rus,ru,jpn,eng","subtitleLanguages":"rus,ru",` +
			`"audioDelayMs":-250}`: true,
		`{"profile":"default"}`:                    true,
		`{"hwdec":"auto-copy-safe"}`:               false,
		`{"profile":"ultra"}`:                      false,
		`{"demuxerMaxBytes":1048575}`:              false,
		`{"demuxerMaxBackBytes":2147483648}`:       false,
		`{"cacheSecs":0.5}`:                        false,
		`{"readaheadS":601}`:                       false,
		`{"networkTimeoutS":0}`:                    false,
		`{"streamBufferBytes":4095}`:               false,
		`{"userAgent":"Mozilla/5.0 (X11; Linux)"}`: true,
		`{"userAgent":"\""}`:                       false,
		`{"userAgent":"Mozilla\r\n"}`:              false,
		`{"videoSync":"display-resample-soft"}`:    false,
		`{"tscale":"ewa_lanczossharp"}`:            false,
		`{"scale":"ewa_haasnsoft"}`:                false,
		`{"sharpen":-1}`:                           false,
		`{"audioLanguages":"rus, ru"}`:             false,
		`{"audioLanguages":"rus,russian"}`:         false,
		`{"subtitleLanguages":"ru-RU,eng"}`:        true,
		`{"audioDelayMs":10001}`:                   false,
		// nothing else of mpv's is reachable: not an option name, not a filter string
		`{"vo":"x11"}`:           false,
		`{"ao":"alsa"}`:          false,
		`{"volume":100}`:         false,
		`{"vf":"gradfun"}`:       false,
		`{"demuxer-lavf-o":"x"}`: false,
	})
	check(t, capschema.MediaMpvState, map[string]bool{
		`{"screen":false}`:             true,
		`{"screen":true,"applied":[]}`: true,
		`{"screen":true,"applied":["profile","cacheSecs"],"pending":["hwdec"],"refused":["userAgent"]}`: true,
		`{}`:                              false,
		`{"screen":true,"dropped":["x"]}`: false,
		`{"screen":true,"refused":["demuxer-lavf-o"]}`: false,
	})
}

func TestSettingsSetArgs(t *testing.T) {
	check(t, capschema.SettingsSetArgs, map[string]bool{
		`{"language":"ru"}`:         true,
		`{"followUpWindowMs":8000}`: true,
		`{"screen":{"overscan":2.5,"subtitles":{"size":"l","style":"outline"},"autoSkipOpenings":true,"reducedMotion":false}}`: true,
		`{}`:                               false,
		`{"screen":{}}`:                    false,
		`{"language":"de"}`:                false,
		`{"followUpWindowMs":1000}`:        false,
		`{"screen":{"overscan":3}}`:        false,
		`{"screen":{"theme":"afterglow"}}`: false,
		`{"volume":80}`:                    false,
	})
	// every screen key it writes is one display.screen's config takes, with the same bounds
	check(t, capschema.DisplayScreenConfig, map[string]bool{
		`{"overscan":2.5,"subtitles":{"size":"l","style":"outline"},"autoSkipOpenings":true,"reducedMotion":false}`: true,
	})
}

func TestInputRemote(t *testing.T) {
	check(t, capschema.InputRemoteConfig, map[string]bool{`{}`: true, `{"cec":true,"longPressMs":500}`: true, `{"longPressMs":10}`: false})
	check(t, capschema.InputRemoteState, map[string]bool{`{"lastSource":"cec"}`: true, `{"lastSource":"voice"}`: false, `{}`: false})
	check(t, capschema.InputRemoteCommands["key"], map[string]bool{
		`{"key":"ok"}`:                        true,
		`{"key":"assistant","action":"long"}`: true,
		`{"key":"volume_up"}`:                 true,
		`{"key":"OK"}`:                        false,
		`{"key":"ok","action":"repeat"}`:      false,
		`{}`:                                  false,
	})
	check(t, capschema.InputRemoteCommands["text"], map[string]bool{`{"value":"frieren"}`: true, `{"value":""}`: false})
}

func TestMediaScreencast(t *testing.T) {
	check(t, capschema.MediaScreencastConfig, map[string]bool{
		`{}`: true,
		`{"enabled":true,"name":"Cadence TV","pin":true,"maxHeight":1080,"h265":true}`: true,
		`{"maxHeight":1440}`: false,
		`{"name":""}`:        false,
	})
	check(t, capschema.MediaScreencastState, map[string]bool{
		`{"enabled":true,"state":"ready","mirroring":true,"clientName":"iPhone"}`: true,
		`{"enabled":false,"state":"off","mirroring":false}`:                       true,
		`{"enabled":true,"state":"ready"}`:                                        false,
	})
}

func TestMediaPlayerAndOutput(t *testing.T) {
	check(t, capschema.MediaPlayerItemActionArgs, map[string]bool{
		`{"sessionId":"s","actionId":"a"}`: true,
		`{"sessionId":"s"}`:                false,
	})
	check(t, capschema.MediaPlayerJumpArgs, map[string]bool{`{"ref":"e:6"}`: true, `{"sessionId":"s","ref":""}`: false})
	check(t, capschema.AudioOutputScreenConfig, map[string]bool{
		`{}`: true, `{"volume":{"mode":"system"}}`: true, `{"volume":{"mode":"hdmi"}}`: false,
		`{"delayMs":-40,"volume":{"mode":"software"}}`: true, `{"delayMs":1001}`: false, `{"delayMs":1.5}`: false,
	})
	check(t, capschema.AudioOutputConfig, map[string]bool{
		`{}`: true, `{"delayMs":120}`: true, `{"delayMs":-1000}`: true, `{"delayMs":-1001}`: false,
		`{"volume":{"mode":"system"}}`: false,
	})
}

func TestMediaGroupConfig(t *testing.T) {
	check(t, capschema.MediaGroupConfig, map[string]bool{
		`{"enabled":false}`: true,
		`{}`:                false,
		`{"enabled":true}`:  false,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"members":["b"]}`:                              true,
		`{"enabled":true,"groupId":"g","role":"member","latencyMs":1000}`:                                              false,
		`{"enabled":true,"groupId":"g","role":"member","latencyMs":1000,"leader":{"host":"192.168.1.2","port":7701}}`:  true,
		`{"enabled":true,"groupId":"g","role":"member","latencyMs":100,"leader":{"host":"192.168.1.2","port":7701}}`:   false,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"channel":"left","trimMs":-20,"budgetUs":100}`: true,
		`{"enabled":true,"groupId":"g","role":"leader","latencyMs":1000,"channel":"centre"}`:                           false,
		`{"enabled":false,"extra":1}`: false,
	})
}

func TestMediaGroupState(t *testing.T) {
	check(t, capschema.MediaGroupState, map[string]bool{
		`{"state":"off"}`: true,
		`{"state":"playing","role":"leader","synced":true,"clockErrorUs":12,"members":[{"deviceId":"b","state":"playing","connected":true}]}`: true,
		`{"state":"dancing"}`: false,
		`{}`:                  false,
	})
}

func TestGroupCalibrateArgs(t *testing.T) {
	check(t, capschema.GroupCalibrateArgs, map[string]bool{
		`{}`:                  true,
		`{"mode":"mics"}`:     true,
		`{"mode":"listener"}`: true,
		`{"mode":"listener","boost":{"m-1":6,"m-2":14}}`:                    true,
		`{"boost":{"m-1":0}}`:                                               true,
		`{"mode":"listener_result","runId":7}`:                              true,
		`{"mode":"listener_result","runId":0}`:                              true,
		`{"mode":"listener_result"}`:                                        false,
		`{"mode":"listener_result","runId":7,"boost":{"m-1":6}}`:            false,
		`{"mode":"listener","runId":7}`:                                     false,
		`{"runId":7}`:                                                       false,
		`{"boost":{"m-1":15}}`:                                              false,
		`{"boost":{"m-1":-1}}`:                                              false,
		`{"boost":{"m-1":1.5}}`:                                             false,
		`{"boost":{"":6}}`:                                                  false,
		`{"boost":{"a":1,"b":1,"c":1,"d":1,"e":1,"f":1,"g":1,"h":1,"i":1}}`: false,
		`{"mode":"room"}`:                                                   false,
		`{"mode":"mics","x":1}`:                                             false,
	})
}
