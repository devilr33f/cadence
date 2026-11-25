package uniproxy

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// uniproxy takes the mic as ogg opus and answers synthesis in it

const (
	oggHeaderBytes    = 27
	oggChecksumOffset = 22
	oggSegmentsOffset = 26
	oggMaxSegments    = 255
	oggFlagContinued  = 0x01
	oggFlagBOS        = 0x02
	oggReservedFlags  = 0xF8
	// granule counts 48 kHz samples whatever the input rate: 20 ms
	oggGranuleStep = 960
	oggInputRate   = 16000
	// a packet past this is dropped rather than held
	oggMaxPacketBytes = 1 << 20
	oggHeaderPackets  = 2
)

var (
	oggCapture = []byte("OggS")
	opusHead   = []byte("OpusHead")
	opusTags   = []byte("OpusTags")

	errPacketTooBig = errors.New("uniproxy: packet does not fit one ogg page")
)

var oggCRCTable = func() (t [256]uint32) {
	for n := range t {
		crc := uint32(n) << 24 //nolint:gosec // n < 256
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
		t[n] = crc
	}
	return t
}()

// oggCRC folds page as if its checksum field were zero
func oggCRC(page []byte) uint32 {
	var crc uint32
	for i, b := range page {
		if i >= oggChecksumOffset && i < oggChecksumOffset+4 {
			b = 0
		}
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}

func oggPage(serial, sequence uint32, flags byte, granule uint64, packet []byte) ([]byte, error) {
	segments := len(packet)/255 + 1
	if segments > oggMaxSegments {
		return nil, errPacketTooBig
	}
	page := make([]byte, oggHeaderBytes+segments+len(packet))
	copy(page, oggCapture)
	page[5] = flags
	binary.LittleEndian.PutUint64(page[6:], granule)
	binary.LittleEndian.PutUint32(page[14:], serial)
	binary.LittleEndian.PutUint32(page[18:], sequence)
	page[oggSegmentsOffset] = byte(segments)
	for i := range segments - 1 {
		page[oggHeaderBytes+i] = 255
	}
	page[oggHeaderBytes+segments-1] = byte(len(packet) % 255)
	copy(page[oggHeaderBytes+segments:], packet)
	binary.LittleEndian.PutUint32(page[oggChecksumOffset:], oggCRC(page))
	return page, nil
}

// oggMuxer wraps one utterance's 20 ms opus packets, one page each
type oggMuxer struct {
	serial   uint32
	sequence uint32
	granule  uint64
}

func newOggMuxer(serial uint32) *oggMuxer {
	return &oggMuxer{serial: serial, sequence: oggHeaderPackets}
}

func (m *oggMuxer) headerPages() []byte {
	head := make([]byte, 19)
	copy(head, opusHead)
	head[8] = 1 // version
	head[9] = 1 // mono
	binary.LittleEndian.PutUint32(head[12:], oggInputRate)
	vendor := []byte("cadence")
	tags := make([]byte, 16+len(vendor))
	copy(tags, opusTags)
	binary.LittleEndian.PutUint32(tags[8:], uint32(len(vendor))) //nolint:gosec // fixed
	copy(tags[12:], vendor)
	h, _ := oggPage(m.serial, 0, oggFlagBOS, 0, head)
	t, _ := oggPage(m.serial, 1, 0, 0, tags)
	return append(h, t...)
}

func (m *oggMuxer) page(packet []byte) ([]byte, error) {
	p, err := oggPage(m.serial, m.sequence, 0, m.granule+oggGranuleStep, packet)
	if err != nil {
		return nil, err
	}
	m.sequence++
	m.granule += oggGranuleStep
	return p, nil
}

// oggDemuxer turns ogg bytes, in whatever pieces they arrive, into opus packets (the two header packets of each
// stream left out). it resynchronises on the capture pattern past corrupt or lost pages and never holds more than one
// packet's worth beyond the bytes not yet framed
type oggDemuxer struct {
	pending     []byte
	held        []byte
	discarding  bool
	headersLeft int
	nextSeq     uint32
	seqKnown    bool
	// how far behind a stalled candidate earlier pushes already looked
	probeAt int
}

func newOggDemuxer() *oggDemuxer { return &oggDemuxer{headersLeft: oggHeaderPackets} }

// pageEnd is the end of the page at at: -1 when the bytes there cannot be a page header, past len(data) while its
// segment table or body is not all here yet
func pageEnd(data []byte, at int) int {
	if len(data)-at < oggHeaderBytes {
		return len(data) + 1
	}
	if data[at+4] != 0 || data[at+5]&oggReservedFlags != 0 {
		return -1
	}
	tableEnd := at + oggHeaderBytes + int(data[at+oggSegmentsOffset])
	if len(data) < tableEnd {
		return len(data) + 1
	}
	end := tableEnd
	for i := at + oggHeaderBytes; i < tableEnd; i++ {
		end += int(data[i])
	}
	return end
}

func checksumOK(page []byte) bool {
	return binary.LittleEndian.Uint32(page[oggChecksumOffset:]) == oggCRC(page)
}

func indexCapture(data []byte, from int) int {
	if from >= len(data) {
		return -1
	}
	i := bytes.Index(data[from:], oggCapture)
	if i < 0 {
		return -1
	}
	return from + i
}

func (d *oggDemuxer) push(in []byte) [][]byte {
	data := append(d.pending, in...)
	var packets [][]byte
	at := 0
	for {
		c := indexCapture(data, at)
		if c < 0 {
			// keep a tail that could be the start of a capture split across pushes
			at = max(at, len(data)-(len(oggCapture)-1))
			break
		}
		at = c
		end := pageEnd(data, at)
		if end < 0 {
			at += len(oggCapture)
			continue
		}
		if end > len(data) {
			// a false capture claiming a long page would hold back every page behind it
			next := d.pageBehind(data, at)
			if next < 0 {
				break
			}
			at = next
			continue
		}
		page := data[at:end]
		if !checksumOK(page) {
			at += len(oggCapture)
			continue
		}
		packets = d.take(page, packets)
		at = end
	}
	d.probeAt = max(d.probeAt-at, 0)
	d.pending = append([]byte(nil), data[at:]...)
	return packets
}

// pageBehind is the first whole page with a good checksum behind a candidate still incomplete, or -1
func (d *oggDemuxer) pageBehind(data []byte, candidate int) int {
	from := max(candidate+len(oggCapture), d.probeAt)
	for {
		at := indexCapture(data, from)
		if at < 0 {
			d.probeAt = max(from, len(data)-(len(oggCapture)-1))
			return -1
		}
		end := pageEnd(data, at)
		if end > len(data) {
			d.probeAt = at
			return -1
		}
		if end >= 0 && checksumOK(data[at:end]) {
			return at
		}
		from = at + len(oggCapture)
	}
}

func (d *oggDemuxer) take(page []byte, packets [][]byte) [][]byte {
	segments := int(page[oggSegmentsOffset])
	flags := page[5]
	seq := binary.LittleEndian.Uint32(page[18:])
	lost := d.seqKnown && seq != d.nextSeq
	d.nextSeq, d.seqKnown = seq+1, true

	switch {
	case flags&oggFlagBOS != 0:
		d.drop()
		d.headersLeft = oggHeaderPackets
	case flags&oggFlagContinued != 0:
		// a continuation with nothing to continue, or after a lost page, is a tail we cannot rebuild
		if !d.inPacket() || lost {
			d.discard()
		}
	default:
		d.drop()
	}
	off := oggHeaderBytes + segments
	for i := range segments {
		n := int(page[oggHeaderBytes+i])
		if !d.discarding {
			d.held = append(d.held, page[off:off+n]...)
		}
		off += n
		if n == 255 {
			if len(d.held) > oggMaxPacketBytes {
				d.discard()
			}
			continue
		}
		if !d.discarding {
			packets = d.finish(packets)
		}
		d.drop()
	}
	return packets
}

func (d *oggDemuxer) finish(packets [][]byte) [][]byte {
	p := d.held
	if d.headersLeft > 0 && (bytes.HasPrefix(p, opusHead) || bytes.HasPrefix(p, opusTags)) {
		d.headersLeft--
		return packets
	}
	d.headersLeft = 0
	return append(packets, p)
}

func (d *oggDemuxer) inPacket() bool { return len(d.held) > 0 || d.discarding }

func (d *oggDemuxer) drop() {
	d.held = nil
	d.discarding = false
}

func (d *oggDemuxer) discard() {
	d.held = nil
	d.discarding = true
}
