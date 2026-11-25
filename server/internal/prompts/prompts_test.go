package prompts

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/audio"
	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

func TestTheTableHasEveryPromptInBothLanguages(t *testing.T) {
	all, err := Table()
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"ru", "en"} {
		var ids []string
		for _, p := range For(all, lang) {
			ids = append(ids, p.ID)
		}
		slices.Sort(ids)
		if want := []string{"failed", "not_heard", "offline", "reminder"}; !slices.Equal(ids, want) {
			t.Fatalf("%s: %v, want %v", lang, ids, want)
		}
	}
}

func TestParseRefusesABadTable(t *testing.T) {
	for _, src := range []string{
		"",
		"offline\tru\tx\n",
		"id\tlang\ttext\nOffline\tru\tx\n",
		"id\tlang\ttext\noffline\trus1\tx\n",
		"id\tlang\ttext\noffline\tru\n",
		"id\tlang\ttext\noffline\tru\t \n",
		"id\tlang\ttext\noffline\tru\ta\noffline\tru\tb\n",
		"id\tlang\ttext\noffline\tru\t" + strings.Repeat("x", 201) + "\n",
	} {
		if _, err := Parse(strings.NewReader(src)); !errors.Is(err, ErrTable) {
			t.Errorf("%q: err %v", src, err)
		}
	}
	got, err := Parse(strings.NewReader("# note\nid\tlang\ttext\r\n\nfailed\ten\tOops.\r\n"))
	if err != nil || len(got) != 1 || got[0] != (Prompt{ID: "failed", Lang: "en", Text: "Oops."}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

// oggPage is a page read back
type oggPage struct {
	flags   byte
	granule int64
	seq     uint32
	packets [][]byte
}

func readPages(t *testing.T, b []byte) []oggPage {
	t.Helper()
	var out []oggPage
	var partial []byte
	for len(b) > 0 {
		if len(b) < 27 || string(b[:4]) != "OggS" {
			t.Fatalf("no page at %d bytes left", len(b))
		}
		n := int(b[26])
		size := 27 + n
		for _, l := range b[27 : 27+n] {
			size += int(l)
		}
		pg := slices.Clone(b[:size])
		crc := binary.LittleEndian.Uint32(pg[22:])
		clear(pg[22:26])
		if oggCRC(pg) != crc {
			t.Fatal("crc")
		}
		p := oggPage{flags: b[5], granule: int64(binary.LittleEndian.Uint64(b[6:])), seq: binary.LittleEndian.Uint32(b[18:])}
		body := b[27+n : size]
		for _, l := range b[27 : 27+n] {
			partial = append(partial, body[:l]...)
			body = body[l:]
			if l < 255 {
				p.packets = append(p.packets, partial)
				partial = nil
			}
		}
		out = append(out, p)
		b = b[size:]
	}
	return out
}

type fakeTTS struct {
	pcm    []int16
	spoken string
	opts   providers.TTSOptions
}

func (f *fakeTTS) Open(_ context.Context, o providers.TTSOptions) (providers.TTSStream, error) {
	f.opts = o
	return &fakeStream{tts: f, audio: make(chan []int16, 1)}, nil
}

type fakeStream struct {
	tts   *fakeTTS
	audio chan []int16
}

func (s *fakeStream) Speak(text string) error { s.tts.spoken = text; return nil }
func (s *fakeStream) Flush() error {
	s.audio <- s.tts.pcm
	close(s.audio)
	return nil
}
func (s *fakeStream) Audio() <-chan []int16 { return s.audio }
func (s *fakeStream) Err() error            { return nil }
func (s *fakeStream) Close() error          { return nil }

// fakeEncoder makes a 3-byte packet of each 20 ms frame at 24 kHz
type fakeEncoder struct{ frames int }

func (e *fakeEncoder) Encode(pcm []int16) ([]byte, error) {
	if len(pcm) != 480 {
		return nil, audio.ErrFrameSize
	}
	e.frames++
	return []byte{0xf8, byte(e.frames), 0}, nil
}
func (e *fakeEncoder) FrameSamples() int { return 480 }

func speech(silentBefore, loud, silentAfter int) []int16 {
	pcm := make([]int16, silentBefore+loud+silentAfter)
	for i := range loud {
		pcm[silentBefore+i] = 4000
	}
	return pcm
}

func TestRenderWrapsTheSpeechInOggOpus(t *testing.T) {
	tts := &fakeTTS{pcm: speech(24000, 24000, 12000)}
	file, err := Render(context.Background(), tts, "v1", Prompt{ID: "failed", Lang: "ru", Text: "Не получилось."},
		func() (audio.Encoder, error) { return &fakeEncoder{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if tts.opts != (providers.TTSOptions{Voice: "v1", SampleRate: 24000, Language: "ru"}) || tts.spoken != "Не получилось." {
		t.Fatalf("opts %+v, spoken %q", tts.opts, tts.spoken)
	}
	pages := readPages(t, file)
	if len(pages) < 3 || pages[0].flags != 0x02 || string(pages[0].packets[0][:8]) != "OpusHead" ||
		string(pages[1].packets[0][:8]) != "OpusTags" {
		t.Fatalf("headers: %+v", pages[:min(2, len(pages))])
	}
	head := pages[0].packets[0]
	if head[9] != 1 || binary.LittleEndian.Uint16(head[10:]) != preSkip || head[18] != 0 {
		t.Fatalf("OpusHead %v", head)
	}
	var audioPackets int
	for i, p := range pages {
		if p.seq != uint32(i) {
			t.Fatalf("page %d has sequence %d", i, p.seq)
		}
		if i >= 2 {
			audioPackets += len(p.packets)
		}
	}
	// 1 s of speech and 50 ms on each side, at 48 kHz, after the pre-skip
	last := pages[len(pages)-1]
	if want := int64(preSkip + 2*(24000+2*1200)); last.flags != 0x04 || last.granule != want {
		t.Fatalf("last page flags %x granule %d, want 4 and %d", last.flags, last.granule, want)
	}
	if int64(audioPackets)*960 < last.granule {
		t.Fatalf("%d packets hold less than the granule %d", audioPackets, last.granule)
	}
}

func TestRenderRefusesSilenceAndLongSpeech(t *testing.T) {
	enc := func() (audio.Encoder, error) { return &fakeEncoder{}, nil }
	if _, err := Render(context.Background(), &fakeTTS{pcm: make([]int16, 24000)}, "", Prompt{Lang: "en"}, enc); !errors.Is(err, ErrNoAudio) {
		t.Fatalf("silence: %v", err)
	}
	if _, err := Render(context.Background(), &fakeTTS{pcm: speech(0, 24000*3, 0)}, "", Prompt{Lang: "en"}, enc); !errors.Is(err, ErrTooLong) {
		t.Fatalf("3 s: %v", err)
	}
}
