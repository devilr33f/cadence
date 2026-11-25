package deepgram

import (
	"log/slog"
	"strconv"

	"github.com/coder/websocket"

	"git.shino.boo/devilreef/cadence/server/internal/tracing"
)

// readDetail is why a socket's read failed, as a token: its close status, else the error's class
func readDetail(err error) string {
	if s := websocket.CloseStatus(err); s != -1 {
		return "closed_" + strconv.Itoa(int(s))
	}
	return "closed_" + tracing.ErrorCode(err)
}

// dialDetail is why a dial failed: the refused upgrade's status, else the error's class
func dialDetail(status int, err error) string {
	if status != 0 {
		return "dial_status_" + strconv.Itoa(status)
	}
	return "dial_" + tracing.ErrorCode(err)
}

// logFailed is a stream's one failure line: codes and numbers alone
func logFailed(log *slog.Logger, kind, module, detail string) {
	if log == nil {
		log = slog.Default()
	}
	log.Warn(kind+"_stream_failed", "module", module, "detail", detail)
}
