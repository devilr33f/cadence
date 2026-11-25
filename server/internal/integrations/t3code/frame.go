package t3code

import (
	"bytes"
	"encoding/json"
	"errors"
)

// frame tags of effect's rpc protocol (see doc.go)
const (
	tagRequest             = "Request"
	tagAck                 = "Ack"
	tagInterrupt           = "Interrupt"
	tagPing                = "Ping"
	tagPong                = "Pong"
	tagEof                 = "Eof"
	tagChunk               = "Chunk"
	tagExit                = "Exit"
	tagDefect              = "Defect"
	tagClientProtocolError = "ClientProtocolError"
)

// Frame is one effect rpc message either way; fields a tag does not use stay
// empty. RequestID and ID hold the raw JSON id (a number or a string).
type Frame struct {
	Tag       string            `json:"_tag"`
	ID        json.RawMessage   `json:"id,omitempty"`
	Method    string            `json:"tag,omitempty"`
	Payload   json.RawMessage   `json:"payload,omitempty"`
	Headers   *[][2]string      `json:"headers,omitempty"`
	RequestID json.RawMessage   `json:"requestId,omitempty"`
	Values    []json.RawMessage `json:"values,omitempty"`
	Exit      *ExitValue        `json:"exit,omitempty"`
	// RequestIDs names the requests a ClientProtocolError ends; nil is all
	RequestIDs []json.RawMessage `json:"requestIds,omitempty"`
}

// ExitValue is an encoded Exit: Success with Value, or Failure with Cause.
type ExitValue struct {
	Tag   string          `json:"_tag"`
	Value json.RawMessage `json:"value,omitempty"`
	Cause []CauseEntry    `json:"cause,omitempty"`
}

// CauseEntry is one part of a failure: Fail (Error, a tagged error object),
// Die (Defect) or Interrupt.
type CauseEntry struct {
	Tag    string          `json:"_tag"`
	Error  json.RawMessage `json:"error,omitempty"`
	Defect json.RawMessage `json:"defect,omitempty"`
}

var errBadFrame = errors.New("t3code: bad frame")

// DecodeFrames reads one WebSocket message: a frame object or an array of
// them. Unknown fields are ignored; a frame without a tag fails the message.
func DecodeFrames(b []byte) ([]Frame, error) {
	t := bytes.TrimSpace(b)
	if len(t) == 0 {
		return nil, errBadFrame
	}
	var out []Frame
	if t[0] == '[' {
		if err := json.Unmarshal(t, &out); err != nil {
			return nil, errBadFrame
		}
	} else {
		var f Frame
		if err := json.Unmarshal(t, &f); err != nil {
			return nil, errBadFrame
		}
		out = []Frame{f}
	}
	for _, f := range out {
		if f.Tag == "" {
			return nil, errBadFrame
		}
	}
	return out, nil
}

// EncodeFrame writes one frame as its own message.
func EncodeFrame(f Frame) ([]byte, error) { return json.Marshal(f) }

func requestFrame(id int64, method string, payload json.RawMessage) Frame {
	return Frame{Tag: tagRequest, ID: idJSON(id), Method: method, Payload: payload, Headers: &[][2]string{}}
}

func ackFrame(id int64) Frame       { return Frame{Tag: tagAck, RequestID: idJSON(id)} }
func interruptFrame(id int64) Frame { return Frame{Tag: tagInterrupt, RequestID: idJSON(id)} }

func idJSON(id int64) json.RawMessage {
	b, _ := json.Marshal(id)
	return b
}

// frameID reads a request id the client sent as a number; the server echoes
// it as sent, but a string form of the same number is taken too.
func frameID(raw json.RawMessage) (int64, bool) {
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if json.Unmarshal([]byte(s), &n) == nil {
			return n, true
		}
	}
	return 0, false
}

// failTag is the _tag of the first typed error in a failure, else "".
func (e *ExitValue) failTag() string {
	for _, c := range e.Cause {
		if c.Tag == "Fail" {
			var t struct {
				Tag string `json:"_tag"`
			}
			if json.Unmarshal(c.Error, &t) == nil {
				return t.Tag
			}
			return ""
		}
	}
	return ""
}

// interrupted reports a failure made of interrupts only.
func (e *ExitValue) interrupted() bool {
	if len(e.Cause) == 0 {
		return false
	}
	for _, c := range e.Cause {
		if c.Tag != "Interrupt" {
			return false
		}
	}
	return true
}
