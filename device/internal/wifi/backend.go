// Package wifi is the network.wifi capability: it joins a wi-fi network the
// server's config names, through a board's own wi-fi mechanism (yandex boards:
// wpa_supplicant's control socket; the sim: a fake). it never removes a saved
// network, so the network the device already holds stays as the fallback a
// failed switch rolls back to.
package wifi

import "context"

// association states as the state schema spells them
const (
	StateCompleted    = "completed"
	StateAssociating  = "associating"
	StateScanning     = "scanning"
	StateDisconnected = "disconnected"
	StateInactive     = "inactive"
	StateUnknown      = "unknown"
)

// Status is the link now: the association the supplicant holds and the
// interface's address. CurrentID is the saved network holding it, -1 none.
type Status struct {
	State     string
	SSID      string
	BSSID     string
	FreqMHz   int
	IP        string
	CurrentID int
}

// AP is one scan result. Security is the supplicant's raw flags string
// ("[WPA2-PSK-CCMP][ESS]"); an SSID of "" is a hidden network.
type AP struct {
	SSID      string
	BSSID     string
	FreqMHz   int
	SignalDbm int
	Security  string
}

// Saved is one network the supplicant holds. BSSID is "any" when unpinned;
// Disabled is out of the supplicant's own choice (a rollback disables the
// network it gave up on, never removing it).
type Saved struct {
	ID       int
	SSID     string
	BSSID    string
	Disabled bool
}

// Backend is a board's wi-fi mechanism. every call is one short-lived command;
// none of them removes a network.
type Backend interface {
	Status(ctx context.Context) (Status, error)
	List(ctx context.Context) ([]Saved, error)
	// Upsert adds the network, or rewrites the one saved with the same ssid and
	// bssid pin, leaving every other alone. a bssid of "" pins none. it enables
	// the network and returns its id.
	Upsert(ctx context.Context, ssid, psk, bssid string, priority int) (int, error)
	// Select makes the network active, keeping the others as enabled or
	// disabled as they were, and persists the supplicant's config.
	Select(ctx context.Context, id int) error
	// Disable keeps the network saved but stops the supplicant choosing it on
	// its own (a network a switch gave up on), and persists the config. an
	// Upsert or a Select enables it again.
	Disable(ctx context.Context, id int) error
	// Scan triggers a scan and returns its results, best signal first.
	Scan(ctx context.Context) ([]AP, error)
}
