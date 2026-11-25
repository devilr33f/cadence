package tools

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// btDevice is a paired (list) or nearby (scan) device as its host reports it; the address never leaves the host
type btDevice struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	Paired    bool   `json:"paired"`
	Connected bool   `json:"connected"`
	Playing   bool   `json:"playing,omitempty"`
	RSSI      *int   `json:"rssi,omitempty"`
}

// list reads the target's devices: list (paired) or scan (nearby), both reads with no session
func (b btCall) list(capability, name string) ([]btDevice, *agent.ToolError) {
	args := []byte("{}")
	// a scan is a write (the radio inquires): the device holds it to the turn's session, as every other write
	if name == "scan" && b.call.SessionID != "" {
		args, _ = json.Marshal(map[string]string{"sessionId": b.call.SessionID})
	}
	res, err := b.d.Command(b.ctx, b.call.DeviceID, capability, name, args)
	if err != nil {
		return nil, bluetoothError(err, true)
	}
	var out struct {
		Devices []btDevice `json:"devices"`
	}
	if json.Unmarshal(res, &out) != nil {
		return nil, btFailed()
	}
	if out.Devices == nil {
		out.Devices = []btDevice{}
	}
	return out.Devices, nil
}

// pick is the device an id or a name means; an id goes as given (the device refuses one it does not know), a name
// is matched against the target's list or scan: exactly, then by its words
func (b btCall) pick(a bluetoothArgs, capability, from string) (btDevice, *agent.ToolError) {
	if a.ID != "" {
		return btDevice{ID: a.ID}, nil
	}
	if a.Name == "" {
		return btDevice{}, badArgs(a.Action + " needs id or name.")
	}
	devs, e := b.list(capability, from)
	if e != nil {
		return btDevice{}, e
	}
	ms := matchBT(devs, a.Name)
	switch len(ms) {
	case 0:
		return btDevice{}, &agent.ToolError{
			Code: "not_found", Message: "No Bluetooth device by that name is known there.",
			Hint: "Use list (or scan) to see the names, or ask the user.", Candidates: btNames(devs),
		}
	case 1:
		return ms[0], nil
	}
	return btDevice{}, &agent.ToolError{
		Code: "ambiguous", Message: "Several Bluetooth devices match that name.",
		Hint: "Ask the user which one they mean, or use its id.", Retryable: true, Candidates: btNames(ms),
	}
}

func matchBT(devs []btDevice, name string) []btDevice {
	key := btKey(name)
	if key == "" {
		return nil
	}
	exact := slices.DeleteFunc(slices.Clone(devs), func(x btDevice) bool { return btKey(x.Name) != key })
	if len(exact) > 0 {
		return exact
	}
	return slices.DeleteFunc(slices.Clone(devs), func(x btDevice) bool { return !btWords(key, btKey(x.Name)) })
}

// btWords is every word of the shorter name in the longer one, or a prefix of one of its words ("jbl" and
// "JBL Flip 6", "айфон" and "Айфон Артёма")
func btWords(a, b string) bool {
	wa, wb := strings.Fields(a), strings.Fields(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	if len(wa) > len(wb) {
		wa, wb = wb, wa
	}
	for _, w := range wa {
		if !slices.ContainsFunc(wb, func(o string) bool { return strings.HasPrefix(o, w) || strings.HasPrefix(w, o) && len(o) >= 3 }) {
			return false
		}
	}
	return true
}

// btKey is a name in lower case, ё as е, separators and punctuation as single spaces
func btKey(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), " ")
}

// btNames are the devices as the model can name them back: name and id
func btNames(devs []btDevice) []string {
	out := make([]string, len(devs))
	for i, x := range devs {
		out[i] = x.Name + " (" + x.ID + ")"
	}
	return out
}
