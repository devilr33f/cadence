package updater

import (
	"encoding/binary"
	"io"
	"os"
	"time"
)

// the ELF e_machine of a build for the board's cpu
const (
	MachineX8664   uint16 = 62
	MachineAArch64 uint16 = 183
)

// magic, class, data, version, os abi, abi version, padding, e_type, then e_machine at 18
const elfHead = 20

// CheckELF: a 64-bit little-endian build for machine; an x86 build, a 32-bit
// one or a script is a wrong download.
func CheckELF(path string, machine uint16) error {
	f, err := os.Open(path) //nolint:gosec // the updater's own soloist.next
	if err != nil {
		return FailIO
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, elfHead)
	if _, err := io.ReadFull(f, head); err != nil {
		return FailFormat
	}
	if string(head[:4]) != "\x7fELF" || head[4] != 2 || head[5] != 1 || binary.LittleEndian.Uint16(head[18:]) != machine {
		return FailFormat
	}
	return nil
}

// Accept: a candidate newer than the installed build, with 7 days left at
// least. The CDN can serve yesterday's build, or one about to stop: neither is
// worth a switch.
func Accept(candidate Build, installed *Build, now time.Time) error {
	newer := installed == nil || candidate.Epoch > installed.Epoch
	lasts := !candidate.ExpiresAt().Before(now.Add(minRemaining))
	if newer && lasts {
		return nil
	}
	return FailStale
}
