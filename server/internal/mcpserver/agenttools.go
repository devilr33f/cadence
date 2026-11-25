package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	cadencev2 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/v2"
	"git.shino.boo/devilreef/cadence/server/internal/agent"
)

// agentExcluded names registered tools an MCP call must not run: the ones a
// turn's own machinery applies (TurnScoped covers them; a name here says why
// besides that).
var agentExcluded = map[string]string{
	// conversation_reset's effect the runner applies when its turn commits;
	// alone the call would answer a reset nothing performs
	"conversation_reset": "a turn's own tool",
}

// mcpDeviceNote is the line the agent tools that take a device argument get
// beside their own description: over MCP there is no "this device", so the
// argument is required and names the device to act as.
const mcpDeviceNote = " Over MCP the device argument is required: name the device (its name, or its room) this call acts on."

// agentTools serves the agent's own tools as MCP tools, run under a
// synthetic pressed call: the same results, queues and rules a voice turn
// gets (rule 7's stance included — the caller is an owner's token, the
// dashboard's typed turns' equal).
func (s *Server) agentTools(take func(definition)) {
	if s.d.Tools == nil {
		return
	}
	for _, t := range s.d.Tools() {
		if t.Run == nil || t.TurnScoped() || agentExcluded[t.Name] != "" || t.Name == "" {
			continue
		}
		take(definition{
			name:        t.Name,
			description: describeFor(t),
			schema:      json.RawMessage(t.Input),
			annotations: annotations(t),
			call:        s.callAgent(t),
		})
	}
}

// describeFor is the tool's own description, its live one when it has one,
// with the MCP device note where its arguments take a device.
func describeFor(t agent.Tool) string {
	d := t.Description
	if t.Describe != nil {
		d = t.Describe()
	}
	if strings.Contains(t.Input, `"device"`) {
		d += mcpDeviceNote
	}
	return d
}

// annotations carry the agent tool's effects as the client's hints: a Read
// tool is read-only, an idempotent one idempotent.
func annotations(t agent.Tool) *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{ReadOnlyHint: t.Effects == agent.Read}
}

// callAgent runs one agent tool under a synthetic pressed call. The call
// acts as the device its device argument names — the dashboard-assistant
// precedent — so the tool's own target resolution, policy and availability
// checks hold exactly as in a turn.
func (s *Server) callAgent(t agent.Tool) func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
	return func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
		var a struct {
			Device string `json:"device"`
		}
		if len(args) > 0 {
			if err := json.Unmarshal(args, &a); err != nil {
				return toolError("bad_arguments", "The arguments are not a JSON object."), nil
			}
		}
		device, refused := s.resolveDevice(ctx, a.Device)
		if refused != nil {
			return refused, nil
		}
		if strings.Contains(t.Input, `"device"`) && a.Device == "" {
			return toolError("device_required", "This tool needs the device argument: name the device it acts on."), nil
		}
		if terr := agent.Validate(t, args); terr != nil {
			return agentToolError(terr), nil
		}
		if t.Available != nil && s.d.Settings != nil && device != "" {
			v, err := s.d.Settings(ctx, device)
			if err == nil && !t.Available(v) {
				return toolError("unavailable", "This tool's setting names no provider."), nil
			}
		}
		call := agent.Call{
			DeviceID: device, TurnID: "mcp_" + rand.Text(), CallID: "call_" + rand.Text(),
			Trigger: cadencev2.Trigger_TRIGGER_BUTTON, Args: args,
		}
		start := s.now()
		out, terr := t.Run(ctx, call)
		if terr != nil {
			s.logTool(t.Name, start, terr.Code)
			return agentToolError(terr), nil
		}
		s.logTool(t.Name, start, "ok")
		return rawResult(out)
	}
}

// agentToolError is the agent's error as the client's model reads it.
func agentToolError(terr *agent.ToolError) *sdk.CallToolResult {
	msg := terr.Code
	if terr.Message != "" {
		msg += ": " + terr.Message
	}
	if len(terr.Candidates) > 0 {
		msg += " Candidates: " + strings.Join(terr.Candidates, ", ") + "."
	}
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: msg}},
	}
}

// rawResult is the agent tool's own JSON answer as the client reads it.
func rawResult(out json.RawMessage) (*sdk.CallToolResult, error) {
	if len(out) == 0 {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "{}"}}}, nil
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(out)}}}, nil
}
