package uniproxy

import (
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
)

// aapi prefixes every control frame; media frames carry a big-endian stream id instead
var aapi = []byte("AAPI")

const (
	topic                 = "quasar-general"
	opusFormat            = "audio/opus"
	activationType        = "auto_listening"
	eventTypeVoiceInput   = 1
	eventSourceAutoListen = 4
	sourceSoftware        = 3
	sourceTypeVoice       = 2
)

// the EEventType numbers the server may send, packed (each below 0x80); not TEvent's field numbers
var supportedServerEvents = []byte{3, 5, 6, 7, 9, 13, 15, 17, 18, 20, 21, 25}

// stream close reasons: OK, INTERNAL_ERROR, STOP_RECORDING, AUDIO_SOURCE_STOPPED, CANCEL, NO_SPEECH
const (
	closeOK            = 0
	closeStopRecording = 2
	closeCancel        = 4
)

type eventHeader struct {
	messageID, refMessageID string
	streamID                uint64
	hasStreamID             bool
	ack                     uint64
	hasAck                  bool
}

// appInfo is the stock speaker's client identity; the upgrade headers carry the same values
type appInfo struct {
	appID, appVersion, osVersion, platform, uuid, deviceID, lang, deviceModel, deviceManufacturer, firmware string
}

func (a appInfo) encode() []byte {
	var b []byte
	b = str(b, 1, a.appID)
	b = str(b, 2, a.appVersion)
	b = str(b, 3, a.osVersion)
	b = str(b, 4, a.platform)
	b = str(b, 5, a.uuid)
	b = str(b, 6, a.deviceID)
	b = str(b, 7, a.lang)
	b = str(b, 11, a.deviceModel)
	b = str(b, 12, a.deviceManufacturer)
	return str(b, 19, a.firmware)
}

func (h eventHeader) encode() []byte {
	var b []byte
	if h.messageID != "" {
		b = str(b, 1, h.messageID)
	}
	if h.refMessageID != "" {
		b = str(b, 2, h.refMessageID)
	}
	if h.hasStreamID {
		b = integer(b, 4, h.streamID)
	}
	if h.hasAck {
		b = integer(b, 5, h.ack)
	}
	return b
}

// clientEvent is TClientMessage { Event { Header, <body> } }
func clientEvent(h eventHeader, body protowire.Number, m []byte) []byte {
	event := sub(nil, 1, h.encode())
	event = sub(event, body, m)
	return sub(nil, 1, event)
}

func synchronizeState(oauthToken, apiKey string, app appInfo) []byte {
	var m []byte
	m = str(m, 1, oauthToken)
	m = str(m, 2, apiKey)
	m = sub(m, 3, app.encode())
	m = sub(m, 7, supportedServerEvents)
	return clientEvent(eventHeader{messageID: uuid.NewString()}, 5, m)
}

type voiceInputParams struct {
	messageID  string
	streamID   uint32
	requestID  string
	sequence   uint64
	oauthToken string
	app        appInfo
}

func voiceInput(p voiceInputParams) []byte {
	var request []byte
	request = sub(request, 1, integer(nil, 1, eventTypeVoiceInput))
	request = sub(request, 5, str(nil, 1, p.oauthToken))
	request = boolean(request, 6, true)
	request = str(request, 14, activationType)

	var header []byte
	header = str(header, 1, p.requestID)
	header = integer(header, 3, p.sequence)
	var source []byte
	source = integer(source, 1, eventSourceAutoListen)
	source = integer(source, 2, sourceSoftware)
	source = integer(source, 3, sourceTypeVoice)

	var m []byte
	m = sub(m, 1, header)
	m = sub(m, 2, p.app.encode())
	m = sub(m, 3, request)
	m = sub(m, 11, source)
	m = str(m, 13, p.app.firmware)
	// false is proto3's default but is written anyway, as the stock request does
	m = boolean(m, 15, false)
	m = str(m, 18, p.app.lang)
	m = str(m, 19, topic)
	m = boolean(m, 21, true)
	m = str(m, 22, opusFormat)
	m = sub(m, 25, boolean(nil, 1, true))
	return clientEvent(eventHeader{messageID: p.messageID, streamID: uint64(p.streamID), hasStreamID: true}, 3, m)
}

func ttsGenerate(messageID, text, lang, voice string) []byte {
	var m []byte
	m = str(m, 1, lang)
	m = str(m, 3, opusFormat)
	m = str(m, 7, text)
	if voice != "" {
		m = str(m, 8, voice)
	}
	return clientEvent(eventHeader{messageID: messageID}, 13, m)
}

func ack(received eventHeader) []byte {
	return clientEvent(eventHeader{messageID: uuid.NewString()}, 9, sub(nil, 1, received.encode()))
}

func streamClose(streamID uint32, messageID string, reason uint64) []byte {
	var m []byte
	m = integer(m, 1, uint64(streamID))
	m = str(m, 2, messageID)
	m = sub(m, 3, integer(nil, 1, reason))
	return sub(nil, 2, m)
}

// server event names, by TServerMessage.TEvent's oneof field numbers
const (
	evUnknown                  = ""
	evAliceResponse            = "AliceResponse"
	evGoAway                   = "GoAway"
	evPush                     = "Push"
	evEventException           = "EventException"
	evInputStartAck            = "InputStartAck"
	evPing                     = "Ping"
	evAsrResult                = "AsrResult"
	evTtsSpeak                 = "TtsSpeak"
	evInvalidAuth              = "InvalidAuth"
	evSynchronizeStateResponse = "SynchronizeStateResponse"
)

var eventFields = []struct {
	n    protowire.Number
	name string
}{
	{2, evAliceResponse}, {4, evGoAway}, {5, evPush}, {9, evEventException}, {10, evInputStartAck}, {12, evPing},
	{13, evAsrResult}, {14, evTtsSpeak}, {17, evInvalidAuth}, {24, evSynchronizeStateResponse},
}

type recognition struct {
	normalized string
	words      []string
}

type asrResult struct {
	responseCode uint64
	endOfUtt     bool
	recognitions []recognition
}

type ttsSpeak struct {
	format   string
	lazy     bool
	streamID uint32
}

// incoming is one control message: an event, or a stream control (control true)
type incoming struct {
	name   string
	header eventHeader
	asr    *asrResult
	tts    *ttsSpeak

	control     bool
	streamID    uint32
	closeReason uint64
	hasClose    bool
}

func parseHeader(b []byte) (eventHeader, error) {
	f, err := decode(b)
	if err != nil {
		return eventHeader{}, err
	}
	h := eventHeader{messageID: f.string(1), refMessageID: f.string(2)}
	h.streamID, h.hasStreamID = f.varint(4)
	h.ack, h.hasAck = f.varint(5)
	return h, nil
}

func parseAsr(b []byte) (*asrResult, error) {
	f, err := decode(b)
	if err != nil {
		return nil, err
	}
	r := &asrResult{}
	r.responseCode, _ = f.varint(1)
	eou, _ := f.varint(3)
	r.endOfUtt = eou != 0
	for _, rb := range f.repeated(4) {
		rf, err := decode(rb)
		if err != nil {
			return nil, err
		}
		rec := recognition{normalized: rf.string(3)}
		for _, wb := range rf.repeated(2) {
			wf, err := decode(wb)
			if err != nil {
				return nil, err
			}
			rec.words = append(rec.words, wf.string(1))
		}
		r.recognitions = append(r.recognitions, rec)
	}
	return r, nil
}

// parseServerMessage reads a control frame with aapi stripped; an error means malformed
func parseServerMessage(b []byte) (incoming, error) {
	root, err := decode(b)
	if err != nil {
		return incoming{}, err
	}
	if eb, ok := root.bytes(1); ok {
		return parseEvent(eb)
	}
	if cb, ok := root.bytes(2); ok {
		c, err := decode(cb)
		if err != nil {
			return incoming{}, err
		}
		id, _ := c.varint(1)
		in := incoming{control: true, streamID: uint32(id)} //nolint:gosec // uniproxy stream ids are 32-bit
		if closeMsg, ok := c.bytes(3); ok {
			// an empty Close is proto3's default reason, OK
			cf, err := decode(closeMsg)
			if err != nil {
				return incoming{}, err
			}
			in.closeReason, _ = cf.varint(1)
			in.hasClose = true
		}
		return in, nil
	}
	// timings only, or empty: well formed, nothing to act on
	return incoming{name: evUnknown}, nil
}

func parseEvent(b []byte) (incoming, error) {
	ev, err := decode(b)
	if err != nil {
		return incoming{}, err
	}
	hb, _ := ev.bytes(1)
	h, err := parseHeader(hb)
	if err != nil {
		return incoming{}, err
	}
	in := incoming{name: evUnknown, header: h}
	for _, ef := range eventFields {
		body, ok := ev.bytes(ef.n)
		if !ok {
			continue
		}
		in.name = ef.name
		// only the bodies cadence reads are decoded; the rest are named and left alone
		switch ef.name {
		case evAsrResult:
			if in.asr, err = parseAsr(body); err != nil {
				return incoming{}, err
			}
		case evTtsSpeak:
			sf, err := decode(body)
			if err != nil {
				return incoming{}, err
			}
			lazy, _ := sf.varint(2)
			// the media stream is the outer header's; proto3 leaves stream 0 off the wire
			in.tts = &ttsSpeak{format: sf.string(1), lazy: lazy != 0, streamID: uint32(h.streamID)} //nolint:gosec // 32-bit ids
		}
		break
	}
	return in, nil
}
