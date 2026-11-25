// Package uniproxy adapts Yandex UniProxy, the speech service Yandex's own speakers use, to the STT and TTS
// contracts: ported from v1's yandex-uniproxy provider. It runs on the server with the credentials of one speaker
// (D2): an instance per device, its oauth and auth tokens and device id sealed as the instance's secrets, picked by
// that device's stt and tts settings. Upstream text never reaches an error, event or log.
package uniproxy

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const (
	defaultURL              = "wss://uniproxy.alice.yandex.net/uni.ws"
	defaultDeviceModel      = "pickle"
	defaultHandshakeTimeout = 5 * time.Second
	// above the coordinator's 30 s utterance limit, so it never beats the coordinator's own end
	defaultRequestTimeout = 40 * time.Second
)

var (
	deviceIDPattern = regexp.MustCompile(`^[\w.-]{1,64}$`)
	// one per process, so stt and tts present the same client identity, as v1 did
	processUUID = uuid.NewString()

	errCredentials = errors.New("uniproxy: credentials missing or malformed")
)

// config is one instance's resolved settings; lang is set per stream from the turn's language
type config struct {
	url    string
	client *http.Client
	log    *slog.Logger
	module string
	// failCode is the kind's failure code, logged with a recovered panic
	failCode         string
	oauthToken       string
	authToken        string
	deviceID         string
	uid              string
	uuid             string
	firmware         string
	deviceModel      string
	language         string
	voice            string
	lang             string
	handshakeTimeout time.Duration
	requestTimeout   time.Duration
}

const configSchema = `{"type":"object","additionalProperties":false,"required":["firmware"],"properties":{` +
	`"firmware":{"type":"string","pattern":"^[A-Za-z0-9_.+-]{1,64}$"},` +
	`"deviceModel":{"type":"string","pattern":"^[A-Za-z0-9_.-]{1,64}$"},` +
	`"language":{"type":"string","pattern":"^[a-z]{2,3}(-[A-Za-z]{2,4})?$"},` +
	`"uuid":{"type":"string","pattern":"^[0-9a-fA-F-]{1,64}$"},` +
	`"requestTimeoutMs":{"type":"integer","minimum":35000,"maximum":120000}%s}}`

const voiceProperty = `,"voice":{"type":"string","pattern":"^[A-Za-z0-9_.-]{1,64}$"}`

var secrets = []string{"oauthToken", "authToken", "deviceId"}

// STTModule and TTSModule share their config, but for tts's voice; the host is fixed, so the credentials only ever
// go to Yandex
var STTModule = providers.Module{
	ID:              "yandex-uniproxy-stt",
	Kind:            providers.KindSTT,
	ConfigSchema:    strings.Replace(configSchema, "%s", "", 1),
	Secrets:         secrets,
	OptionalSecrets: []string{"uid"},
	Probe:           probe,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		c, err := newConfig(cfg, sec, "yandex-uniproxy-stt", providers.CodeSTTFailed)
		if err != nil {
			return nil, err
		}
		return &stt{cfg: c}, nil
	},
}

var TTSModule = providers.Module{
	ID:              "yandex-uniproxy-tts",
	Kind:            providers.KindTTS,
	ConfigSchema:    strings.Replace(configSchema, "%s", voiceProperty, 1),
	Secrets:         secrets,
	OptionalSecrets: []string{"uid"},
	Probe:           probe,
	New: func(cfg json.RawMessage, sec map[string]string) (any, error) {
		c, err := newConfig(cfg, sec, "yandex-uniproxy-tts", providers.CodeTTSFailed)
		if err != nil {
			return nil, err
		}
		return &tts{cfg: c}, nil
	},
}

func newConfig(raw json.RawMessage, sec map[string]string, module, failCode string) (*config, error) {
	var v struct {
		Firmware         string `json:"firmware"`
		DeviceModel      string `json:"deviceModel"`
		Language         string `json:"language"`
		Voice            string `json:"voice"`
		UUID             string `json:"uuid"`
		RequestTimeoutMs int    `json:"requestTimeoutMs"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
	}
	if sec["oauthToken"] == "" || sec["authToken"] == "" || !deviceIDPattern.MatchString(sec["deviceId"]) {
		return nil, errCredentials
	}
	c := &config{
		url: defaultURL, client: httpx.Client(nil), module: module, failCode: failCode,
		oauthToken: sec["oauthToken"], authToken: sec["authToken"], deviceID: sec["deviceId"], uid: sec["uid"],
		uuid: v.UUID, firmware: v.Firmware, deviceModel: v.DeviceModel, language: v.Language, voice: v.Voice,
		handshakeTimeout: defaultHandshakeTimeout, requestTimeout: defaultRequestTimeout,
	}
	if c.uuid == "" {
		c.uuid = processUUID
	}
	if c.deviceModel == "" {
		c.deviceModel = defaultDeviceModel
	}
	if v.RequestTimeoutMs > 0 {
		c.requestTimeout = time.Duration(v.RequestTimeoutMs) * time.Millisecond
	}
	return c, nil
}

// withLanguage is c for one stream: the instance's language when set, else the turn's as a recognition tag
func (c *config) withLanguage(language string) *config {
	out := *c
	switch {
	case c.language != "":
		out.lang = c.language
	case language == "en":
		out.lang = "en-US"
	case language == "ru", language == "":
		out.lang = "ru-RU"
	default:
		out.lang = language
	}
	return &out
}
