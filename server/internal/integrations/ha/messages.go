package ha

import (
	"encoding/json"
	"maps"
)

// server message types
const (
	typeAuthRequired = "auth_required"
	typeAuthOK       = "auth_ok"
	typeAuthInvalid  = "auth_invalid"
	typeResult       = "result"
	typeEvent        = "event"
	typePong         = "pong"
)

// inbound is the part of a server message the client reads. Home Assistant's
// error message and every other field are never decoded.
type inbound struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	// HAVersion is what auth_required and auth_ok report
	HAVersion string          `json:"ha_version"`
	Success   *bool           `json:"success"`
	Result    json.RawMessage `json:"result"`
	Error     *struct {
		Code string `json:"code"`
	} `json:"error"`
	Event *struct {
		Data json.RawMessage `json:"data"`
	} `json:"event"`
}

func authMessage(token string) ([]byte, error) {
	return json.Marshal(map[string]string{"type": "auth", "access_token": token})
}

// command encodes fields with the id and type set last, so a field can
// override neither.
func command(id int64, typ string, fields map[string]any) ([]byte, error) {
	m := make(map[string]any, len(fields)+2)
	maps.Copy(m, fields)
	m["id"] = id
	m["type"] = typ
	return json.Marshal(m)
}

// outcome turns a result message into its payload or a code.
func outcome(m inbound) (json.RawMessage, error) {
	switch {
	case m.Type != typeResult || m.Success == nil:
		return nil, fail(CodeBadResponse)
	case *m.Success:
		return m.Result, nil
	case m.Error == nil:
		return nil, haError("")
	}
	return nil, haError(m.Error.Code)
}
