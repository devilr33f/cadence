package uniproxy

import (
	"testing"
)

// the VoiceInput carries the stream in its header and the stock request's fields where yandex reads them
func TestVoiceInputFields(t *testing.T) {
	app := appInfo{uuid: "u", deviceID: testDeviceID, lang: "ru-RU", deviceModel: "yandexmidi", firmware: "1.2.3"}
	b := voiceInput(voiceInputParams{messageID: "m1", streamID: 3, requestID: "r1", sequence: 2, oauthToken: testOAuth, app: app})
	root, err := decode(b)
	if err != nil {
		t.Fatal(err)
	}
	eb, _ := root.bytes(1)
	ev, _ := decode(eb)
	hb, _ := ev.bytes(1)
	h, _ := parseHeader(hb)
	if h.messageID != "m1" || !h.hasStreamID || h.streamID != 3 {
		t.Fatalf("header %+v", h)
	}
	vb, ok := ev.bytes(3)
	if !ok {
		t.Fatal("no VoiceInput body")
	}
	v, _ := decode(vb)
	for n, want := range map[int]string{13: "1.2.3", 18: "ru-RU", 19: topic, 22: opusFormat} {
		if got := v.string(protoNum(n)); got != want {
			t.Errorf("field %d = %q, want %q", n, got, want)
		}
	}
	if eou, ok := v.varint(15); !ok || eou != 0 {
		t.Errorf("field 15 must be written false")
	}
}

func TestParseServerMessages(t *testing.T) {
	ev := sub(nil, 1, eventHeader{refMessageID: "m1", ack: 7, hasAck: true}.encode())
	ev = sub(ev, 13, asr(0, true, " привет "))
	in, err := parseServerMessage(sub(nil, 1, ev))
	if err != nil || in.name != evAsrResult || in.asr == nil || !in.asr.endOfUtt || in.header.refMessageID != "m1" ||
		!in.header.hasAck || in.header.ack != 7 {
		t.Fatalf("asr %+v %v", in, err)
	}
	if got := recognizedText(in.asr.recognitions); got != "привет" {
		t.Fatalf("text %q", got)
	}
	in, err = parseServerMessage(sub(nil, 2, append(integer(nil, 1, 5), sub(nil, 3, nil)...)))
	if err != nil || !in.control || in.streamID != 5 || !in.hasClose || in.closeReason != closeOK {
		t.Fatalf("control %+v %v", in, err)
	}
	if in, err := parseServerMessage(nil); err != nil || in.name != evUnknown {
		t.Fatalf("empty %+v %v", in, err)
	}
	if _, err := parseServerMessage([]byte{0x0a, 0x05, 0x01}); err == nil {
		t.Fatal("truncated message parsed")
	}
}

func TestRecognizedTextFallsBackToWords(t *testing.T) {
	got := recognizedText([]recognition{{normalized: " ", words: []string{" включи", "", "свет "}}})
	if got != "включи свет" {
		t.Fatalf("%q", got)
	}
}
