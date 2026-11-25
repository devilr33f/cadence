// Package tools holds the built-in agent tools.
package tools

import (
	"context"
	"encoding/json"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// SettingsFunc resolves a device's effective settings.
type SettingsFunc func(ctx context.Context, deviceID string) (settings.Values, error)

func unavailable(msg string) *agent.ToolError {
	return &agent.ToolError{Code: "unavailable", Message: msg}
}

func failed(code string) *agent.ToolError {
	return &agent.ToolError{Code: code, Message: "The service did not answer.", Retryable: true}
}

func encode(v any) (json.RawMessage, *agent.ToolError) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, &agent.ToolError{Code: "internal", Message: "The tool failed."}
	}
	return b, nil
}

func badArgs(msg string) *agent.ToolError {
	return &agent.ToolError{
		Code: "invalid_arguments", Message: msg,
		Hint: "Fix the arguments as the schema says and call the tool again.", Retryable: true,
	}
}

func internal() *agent.ToolError {
	return &agent.ToolError{Code: "internal", Message: "The tool failed."}
}
