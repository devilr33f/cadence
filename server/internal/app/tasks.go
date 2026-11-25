package app

import (
	"context"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/agent/tools"
	"git.shino.boo/devilreef/cadence/server/internal/tasks"
	"git.shino.boo/devilreef/cadence/server/internal/voice"
)

// taskTools are the task tools and the active-tasks view, both only when a
// kind can run tasks: without one there is nothing to start or answer.
func taskTools(m *tasks.Manager, settings tools.SettingsFunc) ([]agent.Tool, agent.Tasks) {
	if len(m.Kinds()) == 0 {
		return nil, nil
	}
	return tools.Tasks(tools.TaskDeps{Manager: m, Settings: settings}), m
}

// announcer is the task reporter's view of the voice coordinator, set once
// the coordinator exists: the coordinator tells the reporter when sessions
// and announcements end.
type announcer struct{ c *voice.Coordinator }

func (a *announcer) SessionActive(deviceID string) bool { return a.c.SessionActive(deviceID) }

func (a *announcer) Announcing(deviceID string) bool { return a.c.Announcing(deviceID) }

// Stop is a screen's assistant.stop of the device's session.
func (a *announcer) Stop(deviceID, sessionID string) bool { return a.c.Stop(deviceID, sessionID) }

// Announce speaks in the device's language, as the event turn did.
func (a *announcer) Announce(ctx context.Context, deviceID, text string) error {
	_, err := a.c.Announce(ctx, deviceID, voice.Announcement{Text: text})
	return err
}

// taskKinds is the dashboard's view of the task kinds: the instance the
// running hermes kind was built with at startup (empty: disabled) beside the
// global setting now, which only a restart applies.
func taskKinds(configured func(context.Context) (string, error), instance string) func(context.Context) ([]tasks.KindStatus, error) {
	return func(ctx context.Context) ([]tasks.KindStatus, error) {
		now, err := configured(ctx)
		if err != nil {
			return nil, err
		}
		return []tasks.KindStatus{{Name: "hermes", Instance: instance, Configured: now}}, nil
	}
}
