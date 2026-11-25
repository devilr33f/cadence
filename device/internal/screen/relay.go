package screen

import (
	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/device/internal/tracing"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/pkg/rpc"
)

// LinkUp is core's word that a link to the server is up: the screen's rpc and context go over send from now on, and
// info is what the owner set for the device (its name and room), shown on the screen.
func (s *Server) LinkUp(send func(*cadencev2.Envelope) error, info *cadencev2.DeviceInfo) {
	s.mu.Lock()
	s.uplink = send
	if info != nil {
		s.info = proto.CloneOf(info)
	}
	s.mu.Unlock()
	s.send(s.ready())
	s.send(s.linkStatus())
}

// LinkDown follows each LinkUp once its link ended.
func (s *Server) LinkDown() {
	s.mu.Lock()
	s.uplink = nil
	// the server pushes them again at the next link; one kept past it could show a ring long stopped
	s.alarms = nil
	s.mu.Unlock()
	// the calls in flight are answered by nobody now
	tracing.ReleasePrefix(rpcSpanPrefix, "link_down")
	s.send(s.linkStatus())
}

// LinkMessage takes a frame off the link's read path that is the screen's: an rpc's answer or event, an assistant
// turn, its levels, cards, a duck, the device's info. It never blocks: a screen that is not attached, or does not
// read, loses them.
func (s *Server) LinkMessage(env *cadencev2.Envelope) bool {
	var out *screenv1.Envelope
	switch b := env.GetBody().(type) {
	case *cadencev2.Envelope_RpcResponse:
		rpcAnswered(b.RpcResponse)
		out = &screenv1.Envelope{Body: &screenv1.Envelope_RpcResponse{RpcResponse: b.RpcResponse}}
	case *cadencev2.Envelope_RpcEvent:
		rpcEvent(b.RpcEvent)
		out = &screenv1.Envelope{Body: &screenv1.Envelope_RpcEvent{RpcEvent: b.RpcEvent}}
	case *cadencev2.Envelope_AssistantTurn:
		out = &screenv1.Envelope{Body: &screenv1.Envelope_AssistantTurn{AssistantTurn: b.AssistantTurn}}
	case *cadencev2.Envelope_VoiceLevel:
		out = &screenv1.Envelope{Body: &screenv1.Envelope_VoiceLevel{VoiceLevel: b.VoiceLevel}}
	case *cadencev2.Envelope_Cards:
		out = &screenv1.Envelope{Body: &screenv1.Envelope_Cards{Cards: b.Cards}}
	case *cadencev2.Envelope_Duck:
		// the mix ducks in audiod (or the player, on its own route); the screen shows it
		if s.o.Media != nil {
			s.o.Media.ServerDuck(b.Duck)
		}
		out = &screenv1.Envelope{Body: &screenv1.Envelope_Duck{Duck: b.Duck}}
	case *cadencev2.Envelope_MediaSessionState:
		// the server's fields of a session it owns: kept for a screen that connects later, never sent back up
		st := b.MediaSessionState
		if st.GetSessionId() == "" {
			return true
		}
		s.mu.Lock()
		if len(s.served) >= maxServed {
			clear(s.served)
		}
		s.served[st.GetSessionId()] = proto.CloneOf(st)
		s.mu.Unlock()
		out = &screenv1.Envelope{Body: &screenv1.Envelope_MediaSessionState{MediaSessionState: st}}
	case *cadencev2.Envelope_TasksSummary:
		out = &screenv1.Envelope{Body: &screenv1.Envelope_TasksSummary{TasksSummary: b.TasksSummary}}
	case *cadencev2.Envelope_AlarmsSummary:
		// kept for a screen that connects later: a ring shows on it too
		s.mu.Lock()
		s.alarms = proto.CloneOf(b.AlarmsSummary)
		s.mu.Unlock()
		out = &screenv1.Envelope{Body: &screenv1.Envelope_AlarmsSummary{AlarmsSummary: b.AlarmsSummary}}
	case *cadencev2.Envelope_DeviceInfo:
		s.mu.Lock()
		s.info = proto.CloneOf(b.DeviceInfo)
		s.mu.Unlock()
		out = s.ready()
	default:
		return false
	}
	s.send(out)
	return true
}

// the most server session fields kept; past it the old ones go (a session the screen no longer plays)
const maxServed = 16

// SetServerURL is the origin cadenced links to (its config's serverUrl), shown to the screen in LinkStatus so it can
// fetch images through the server; set before the screen plane runs.
func (s *Server) SetServerURL(origin string) {
	s.mu.Lock()
	s.serverURL = origin
	s.mu.Unlock()
	s.send(s.linkStatus())
}

func (s *Server) linkStatus() *screenv1.Envelope {
	s.mu.Lock()
	up, claiming, origin := s.uplink != nil, s.claim != nil, s.serverURL
	s.mu.Unlock()
	state := screenv1.LinkState_LINK_STATE_OFFLINE
	switch {
	case claiming:
		state = screenv1.LinkState_LINK_STATE_UNENROLLED
	case up:
		state = screenv1.LinkState_LINK_STATE_ONLINE
	}
	return &screenv1.Envelope{Body: &screenv1.Envelope_LinkStatus{LinkStatus: &screenv1.LinkStatus{State: state, ServerUrl: origin}}}
}

// up sends a frame of the screen's to the server; without a link it is dropped (the screen's context is sent again on
// its next change, and at each turn)
func (s *Server) up(env *cadencev2.Envelope) bool {
	s.mu.Lock()
	send := s.uplink
	s.mu.Unlock()
	return send != nil && send(env) == nil
}

// rpc passes a call on to the server; without a link it is answered unavailable at once. its json is never logged
func (s *Server) rpc(r *cadencev2.RpcRequest) {
	rpcStart(r)
	if s.up(&cadencev2.Envelope{Body: &cadencev2.Envelope_RpcRequest{RpcRequest: r}}) {
		return
	}
	rpcUnsent(r)
	s.send(&screenv1.Envelope{Body: &screenv1.Envelope_RpcResponse{RpcResponse: &cadencev2.RpcResponse{
		Id: r.GetId(), Status: rpc.StatusUnavailable, Json: `{"code":"` + rpc.StatusUnavailable + `"}`,
	}}})
}

// Inject hands the screen a key the board read itself (a cec remote's arrows and ok), as if pressed there; false when
// no screen is attached.
func (s *Server) Inject(e *screenv1.InputEvent) bool {
	s.remote.saw(e.GetSource())
	return s.send(&screenv1.Envelope{Body: &screenv1.Envelope_InputEvent{InputEvent: e}})
}
