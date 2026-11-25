package prompts

import (
	"bytes"
	"encoding/binary"
)

// preSkip is libopus' encoder delay, 6.5 ms in 48 kHz samples (RFC 7845 counts the pre-skip at 48 kHz whatever the
// input rate); a prompt that starts a few samples late or early is inaudible
const preSkip = 312

// granulePerPacket is a 20 ms packet in 48 kHz samples
const granulePerPacket = 960

// packetsPerPage keeps every page's lacing short: a page holds at most 255 segments
const packetsPerPage = 25

// serial is the one logical stream's serial number; a file holds nothing else
const serial = 0x43414443 // "CADC"

var crcTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		r := uint32(i) << 24
		for range 8 {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

func oggCRC(b []byte) uint32 {
	var crc uint32
	for _, c := range b {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^c]
	}
	return crc
}

// page is one Ogg page of whole packets
func page(packets [][]byte, flags byte, granule int64, seq uint32) []byte {
	var lacing []byte
	var body bytes.Buffer
	for _, p := range packets {
		n := len(p)
		for n >= 255 {
			lacing = append(lacing, 255)
			n -= 255
		}
		lacing = append(lacing, byte(n)) //nolint:gosec // n < 255 after the loop
		body.Write(p)
	}
	h := make([]byte, 27, 27+len(lacing)+body.Len())
	copy(h, "OggS")
	h[5] = flags
	binary.LittleEndian.PutUint64(h[6:], uint64(granule)) //nolint:gosec // a granule is never negative here
	binary.LittleEndian.PutUint32(h[14:], serial)
	binary.LittleEndian.PutUint32(h[18:], seq)
	h[26] = byte(len(lacing)) //nolint:gosec // packetsPerPage short packets: far under 255 segments
	h = append(h, lacing...)
	h = append(h, body.Bytes()...)
	binary.LittleEndian.PutUint32(h[22:], oggCRC(h))
	return h
}

// oggOpus wraps mono 20 ms Opus packets encoded from inputRate audio in an Ogg Opus file (RFC 7845): OpusHead, an
// empty OpusTags with the vendor "cadence", then the audio, the last page's granule trimming the end to samples (at
// 48 kHz). The packets must each be under 255 * 255 bytes, as any 20 ms packet is.
func oggOpus(packets [][]byte, inputRate int, samples48k int64) []byte {
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1
	head[9] = 1
	binary.LittleEndian.PutUint16(head[10:], preSkip)
	binary.LittleEndian.PutUint32(head[12:], uint32(inputRate)) //nolint:gosec // a sample rate, at most 48000
	tags := []byte("OpusTags")
	tags = binary.LittleEndian.AppendUint32(tags, 7)
	tags = append(tags, "cadence"...)
	tags = binary.LittleEndian.AppendUint32(tags, 0)

	var out bytes.Buffer
	out.Write(page([][]byte{head}, 0x02, 0, 0))
	out.Write(page([][]byte{tags}, 0, 0, 1))
	seq := uint32(2)
	end := preSkip + samples48k
	for i := 0; i < len(packets); i += packetsPerPage {
		j := min(i+packetsPerPage, len(packets))
		granule := min(int64(j)*granulePerPacket, end)
		var flags byte
		if j == len(packets) {
			flags = 0x04
			granule = end
		}
		out.Write(page(packets[i:j], flags, granule, seq))
		seq++
	}
	return out.Bytes()
}
