package t3codetest

import (
	"encoding/json"
	"strings"
)

type wireCommand struct {
	Type           string         `json:"type"`
	CommandID      string         `json:"commandId"`
	ThreadID       string         `json:"threadId"`
	RequestID      string         `json:"requestId"`
	RunID          string         `json:"runId"`
	Decision       string         `json:"decision"`
	Answers        map[string]any `json:"answers"`
	MessageID      string         `json:"messageId"`
	Text           *string        `json:"text"`
	CreatedBy      string         `json:"createdBy"`
	CreationSource string         `json:"creationSource"`
	Attachments    []any          `json:"attachments"`
	DispatchMode   *struct {
		Type        string `json:"type"`
		TargetRunID string `json:"targetRunId"`
	} `json:"dispatchMode"`
}

func dispatchError(cmd wireCommand) obj {
	return failure(obj{"_tag": "OrchestrationV2DispatchCommandError", "commandId": cmd.CommandID, "commandType": cmd.Type, "message": "command refused"})
}

// dispatch applies one command and answers its Exit; a command id seen
// before answers its first sequence again.
func (s *Server) dispatch(raw json.RawMessage) obj {
	var cmd wireCommand
	if json.Unmarshal(raw, &cmd) != nil || cmd.Type == "" || strings.TrimSpace(cmd.CommandID) == "" || cmd.ThreadID == "" {
		return dispatchError(cmd)
	}
	s.mu.Lock()
	s.commands = append(s.commands, Command{Type: cmd.Type, ThreadID: cmd.ThreadID, CommandID: cmd.CommandID, Raw: append(json.RawMessage(nil), raw...)})
	seq, dup := s.byCommand[cmd.CommandID]
	s.mu.Unlock()
	if dup {
		return success(obj{"sequence": seq})
	}
	st := s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	t := st.threads[cmd.ThreadID]
	if t == nil || s.apply(t, cmd) != nil {
		return dispatchError(cmd)
	}
	st.notify()
	s.mu.Lock()
	s.byCommand[cmd.CommandID] = st.seq
	s.mu.Unlock()
	s.cfg.Log.Info("command", "type", cmd.Type, "thread", cmd.ThreadID)
	if s.cfg.OnCommand != nil {
		s.cfg.OnCommand(Command{Type: cmd.Type, ThreadID: cmd.ThreadID, CommandID: cmd.CommandID})
	}
	return success(obj{"sequence": st.seq})
}

// apply runs one command on t; st.mu held.
func (s *Server) apply(t *thread, cmd wireCommand) error {
	st := s.st
	switch cmd.Type {
	case "message.dispatch":
		if cmd.MessageID == "" || cmd.Text == nil || cmd.DispatchMode == nil || cmd.Attachments == nil ||
			cmd.CreatedBy == "" || cmd.CreationSource == "" {
			return errRefused
		}
		switch cmd.DispatchMode.Type {
		case "start_immediately", "queue_after_active":
			if t.activeID != "" {
				if cmd.DispatchMode.Type == "start_immediately" {
					return errRefused
				}
				st.event(cmd.ThreadID, "message.updated", st.message(t, t.activeID, "user", *cmd.Text))
				return nil
			}
		case "steer_active", "restart_active":
			if t.activeID == "" || cmd.DispatchMode.TargetRunID != t.activeID {
				return errRefused
			}
			st.event(cmd.ThreadID, "message.updated", st.message(t, t.activeID, "user", *cmd.Text))
			return nil
		default:
			return errRefused
		}
		st.start(t, *cmd.Text, s.cfg.Step)
	case "runtime-request.respond":
		return st.respond(t, cmd.RequestID, cmd.Decision, cmd.Answers)
	case "run.interrupt":
		if t.activeID == "" || cmd.RunID != t.activeID {
			return errRefused
		}
		st.endRun(t, "interrupted")
	case "thread.stop":
		st.endRun(t, "cancelled")
	default:
		return errRefused
	}
	return nil
}

type wireLaunch struct {
	CommandID      string `json:"commandId"`
	ProjectID      string `json:"projectId"`
	Title          string `json:"title"`
	CreationSource string `json:"creationSource"`
	RuntimeMode    string `json:"runtimeMode"`
	ModelSelection *struct {
		InstanceID string `json:"instanceId"`
		Model      string `json:"model"`
	} `json:"modelSelection"`
	InteractionMode   string `json:"interactionMode"`
	WorkspaceStrategy *struct {
		Type string `json:"type"`
	} `json:"workspaceStrategy"`
	InitialMessage *struct {
		Text string `json:"text"`
	} `json:"initialMessage"`
}

func (s *Server) launch(raw json.RawMessage) obj {
	var in wireLaunch
	ok := json.Unmarshal(raw, &in) == nil
	refuse := failure(obj{"_tag": "OrchestrationV2ThreadLaunchError", "commandId": in.CommandID, "projectId": in.ProjectID, "message": "launch refused"})
	if !ok || in.CommandID == "" || in.ProjectID != ProjectID || strings.TrimSpace(in.Title) == "" || in.RuntimeMode == "" ||
		in.InteractionMode == "" || in.ModelSelection == nil || in.ModelSelection.Model == "" || in.WorkspaceStrategy == nil {
		return refuse
	}
	st := s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	id := st.newID("thread")
	t := st.addThread(id, in.ProjectID, in.Title)
	t.shell["runtimeMode"], t.shell["creationSource"] = in.RuntimeMode, in.CreationSource
	st.shellUpdate(t)
	if in.InitialMessage != nil {
		st.start(t, in.InitialMessage.Text, s.cfg.Step)
	}
	st.notify()
	s.mu.Lock()
	s.commands = append(s.commands, Command{Type: "launchThread", ThreadID: id, CommandID: in.CommandID, Raw: append(json.RawMessage(nil), raw...)})
	s.mu.Unlock()
	s.cfg.Log.Info("command", "type", "launchThread", "thread", id)
	if s.cfg.OnCommand != nil {
		s.cfg.OnCommand(Command{Type: "launchThread", ThreadID: id, CommandID: in.CommandID})
	}
	return success(obj{"threadId": id, "projection": st.projection(t), "resumed": false, "sequence": st.seq})
}
