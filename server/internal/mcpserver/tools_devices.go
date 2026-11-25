package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

// object is a JSON Schema object type with properties, the shape every
// served tool's input takes.
func object(props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type": "object", "properties": props,
		"required":             required,
		"additionalProperties": false,
	}
}

func prop(description string, extra map[string]any) map[string]any {
	p := map[string]any{"type": "string", "description": description}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// deviceTools serves list_devices, get_device and send_command.
func (s *Server) deviceTools(take func(definition)) {
	take(definition{
		name: "list_devices",
		description: "List the household's devices: each one's id, name, room, model, whether it is online and " +
			"the multiroom group it is in. Names are what every tool's device argument takes.",
		schema: object(map[string]any{}),
		call: func(ctx context.Context, _ json.RawMessage) (*sdk.CallToolResult, error) {
			if s.d.List == nil {
				return toolError("unavailable", "No device store."), nil
			}
			list, err := s.d.List(ctx)
			if err != nil {
				return toolError("internal", "The devices could not be read."), nil
			}
			out := make([]map[string]any, 0, len(list))
			for _, d := range list {
				item := map[string]any{"id": d.ID, "name": d.Name, "room": d.Room, "model": d.Model, "status": d.Status,
					"online": s.online(d.ID)}
				if d.LastSeenAt != nil {
					item["lastSeenAt"] = *d.LastSeenAt
				}
				out = append(out, item)
			}
			return textResult(map[string]any{"devices": out})
		},
	})
	take(definition{
		name: "get_device",
		description: "One device in detail: its capabilities with the state each reports (volume, playback, " +
			"availability), its vitals (cpu, memory, disks, temperature) and its daemon version.",
		schema: object(map[string]any{"device": prop("The device's name or id.", nil)}, "device"),
		call: func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
			if s.d.Detail == nil {
				return toolError("unavailable", "No device store."), nil
			}
			device, refused := s.deviceArg(ctx, args)
			if refused != nil {
				return refused, nil
			}
			d, err := s.d.Detail(ctx, device)
			switch {
			case errors.Is(err, devices.ErrNotFound):
				return toolError("unknown_device", "No device of this household has that name."), nil
			case err != nil:
				return toolError("internal", "The device could not be read."), nil
			}
			out := map[string]any{"device": d, "online": s.online(device)}
			if s.d.Vitals != nil {
				if v, ok := s.d.Vitals(device); ok {
					out["vitals"] = v
				}
			}
			return textResult(out)
		},
	})
	take(definition{
		name: "send_command",
		description: "Send one capability command straight to a device: capability and command by name, arguments " +
			"as the device's schema for them describes (the same commands the admin api and Home Assistant send, " +
			"validated against the device's own schema). Prefer the named tools; this is the escape hatch.",
		schema: object(map[string]any{
			"device":     prop("The device's name or id.", nil),
			"capability": prop("The capability's id, e.g. media.player, audio.output, media.url.", nil),
			"command":    prop("The command's name, e.g. pause, set_level, play.", nil),
			"args":       map[string]any{"type": "object", "description": "The command's arguments as its schema describes.", "additionalProperties": true},
		}, "device", "capability", "command"),
		call: func(ctx context.Context, args json.RawMessage) (*sdk.CallToolResult, error) {
			if s.d.Send == nil {
				return toolError("unavailable", "No command service."), nil
			}
			device, refused := s.deviceArg(ctx, args)
			if refused != nil {
				return refused, nil
			}
			var a struct {
				Capability string          `json:"capability"`
				Command    string          `json:"command"`
				Args       json.RawMessage `json:"args"`
			}
			_ = json.Unmarshal(args, &a)
			start := s.now()
			res, err := s.d.Send(ctx, device, a.Capability, a.Command, a.Args)
			if err != nil {
				s.logTool("send_command", start, commandCode(err))
				return toolError(commandCode(err), "The command was refused or failed."), nil
			}
			s.logTool("send_command", start, "ok")
			return rawResult(res)
		},
	})
}

// commandCode is a command failure's class, the same ones the admin api
// answers by.
func commandCode(err error) string {
	var invalid *commands.InvalidArgumentsError
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &invalid):
		return "invalid_arguments"
	case errors.Is(err, devices.ErrNotFound):
		return "unknown_device"
	case errors.Is(err, devices.ErrUnknownCapability):
		return "unknown_capability"
	case errors.Is(err, devices.ErrUnknownCommand):
		return "unknown_command"
	case errors.Is(err, commands.ErrArgumentsTooLarge):
		return "arguments_too_large"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	}
	return "failed"
}

// deviceArg reads the device argument and resolves it; a missing one is
// refused, for these tools have no "this device".
func (s *Server) deviceArg(ctx context.Context, args json.RawMessage) (string, *sdk.CallToolResult) {
	var a struct {
		Device string `json:"device"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &a)
	}
	if a.Device == "" {
		return "", toolError("device_required", "The device argument is required: name the device.")
	}
	id, refused := s.resolveDevice(ctx, a.Device)
	if refused != nil {
		return "", refused
	}
	return id, nil
}

// online says whether the device is linked, unknown when the hub is not
// wired.
func (s *Server) online(deviceID string) bool {
	if s.d.Online == nil {
		return false
	}
	return s.d.Online(deviceID)
}
