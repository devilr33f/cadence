package events

import "git.shino.boo/devilreef/cadence/server/internal/auth"

// Topic is what a live feed client subscribes to (M7a-D7); every event type
// belongs to one.
type Topic string

const (
	TopicDevices  Topic = "devices"
	TopicSessions Topic = "sessions"
	TopicAgent    Topic = "agent"
	TopicTasks    Topic = "tasks"
	TopicMedia    Topic = "media"
	TopicVitals   Topic = "vitals"
	TopicLogs     Topic = "logs"
	TopicPresence Topic = "presence"
	// TopicSubscriptions carries the subscriptions hub's updates, deliveries
	// and inbox changes: ids, topics, events and states, never outside text
	TopicSubscriptions Topic = "subscriptions"
	// TopicGroups carries the multiroom groups' changes and their members' live status: ids, the owner's names and
	// numbers
	TopicGroups Topic = "groups"
	// TopicAlarms carries each device's alarms state and its rings: ids, times, kinds and a reminder's label (the
	// owner's own setting)
	TopicAlarms Topic = "alarms"
	// TopicTraces carries the trace store's span changes (internal/tracing): ids, names, codes, numbers and timings,
	// opt-in as logs are, a feed's events capped a second
	TopicTraces Topic = "traces"
	// TopicOps carries the device-update runs' changes (internal/deviceops): ids, statuses, step
	// indexes, codes and versions
	TopicOps Topic = "ops"
	// TopicWakeWords carries the custom wake words' changes, their trainings' progress and the negatives' fetch
	// (internal/wakewords): ids, statuses, stages, fractions and codes, never a word or audio
	TopicWakeWords Topic = "wakewords"
)

// Topics are every topic, in the order the feed lists them.
var Topics = []Topic{TopicDevices, TopicSessions, TopicAgent, TopicTasks, TopicMedia, TopicVitals, TopicLogs, TopicPresence, TopicSubscriptions, TopicGroups, TopicAlarms, TopicTraces, TopicOps, TopicWakeWords}

// topicScopes are the scope each topic needs besides admin: ha reads devices,
// media and vitals, which carry no transcript, tool or task text; busy changes
// are admin's (the presence scope reaches its routes alone).
var topicScopes = map[Topic]auth.Scope{
	TopicDevices: auth.ScopeHA, TopicMedia: auth.ScopeHA, TopicVitals: auth.ScopeHA,
	TopicSessions: auth.ScopeAdmin, TopicAgent: auth.ScopeAdmin, TopicTasks: auth.ScopeAdmin, TopicLogs: auth.ScopeAdmin,
	TopicPresence:      auth.ScopeAdmin,
	TopicSubscriptions: auth.ScopeAdmin,
	TopicGroups:        auth.ScopeHA,
	TopicAlarms:        auth.ScopeHA,
	TopicTraces:        auth.ScopeAdmin,
	TopicOps:           auth.ScopeAdmin,
	TopicWakeWords:     auth.ScopeAdmin,
}

func (t Topic) allowed(s auth.Scopes) bool { return s.Allows(topicScopes[t]) }

// optIn topics reach only a feed that subscribed to them: log lines and span
// changes would flood every default feed (Ruling M7a-R5)
var optIn = map[Topic]bool{TopicLogs: true, TopicTraces: true}

// topicOf maps every event type to its topic (the README's table); a type
// missing here reaches only an admin feed that never subscribed
var topicOf = map[string]Topic{
	"turn_start": TopicSessions, "transcript": TopicSessions, "turn_end": TopicSessions, AssistantSpeech: TopicSessions,
	"llm_request": TopicAgent, "llm_text": TopicAgent, "tool_call": TopicAgent, "tool_result": TopicAgent, ToolCard: TopicAgent,
	"mode_switch": TopicAgent, "skills_loaded": TopicAgent, "budget_spent": TopicAgent, "memory_recall": TopicAgent,
	MemoryChanged: TopicAgent, "compaction": TopicAgent, "learning_run": TopicAgent, "learning_op": TopicAgent,
	MCPConnected: TopicAgent, MCPUnavailable: TopicAgent, MCPToolsChanged: TopicAgent,
	TaskEvent:    TopicTasks,
	DeviceOnline: TopicDevices, DeviceOffline: TopicDevices, DeviceAdded: TopicDevices, DeviceRenamed: TopicDevices,
	DeviceDeleted: TopicDevices, DeviceVolume: TopicDevices,
	MediaState:         TopicMedia,
	DeviceVitals:       TopicVitals,
	LogLine:            TopicLogs,
	BusyChanged:        TopicPresence,
	SubscriptionUpdate: TopicSubscriptions, SubscriptionChanged: TopicSubscriptions, SubscriptionInbox: TopicSubscriptions,
	GroupChanged: TopicGroups, GroupState: TopicGroups,
	AlarmsState: TopicAlarms, AlarmRinging: TopicAlarms, AlarmRingEnded: TopicAlarms,
	SpanStart: TopicTraces, SpanUpdate: TopicTraces, SpanEnd: TopicTraces,
	"ops_run_started": TopicOps, "ops_step": TopicOps, "ops_run_ended": TopicOps,
	"wakeword_changed": TopicWakeWords, "wakeword_progress": TopicWakeWords, "wakeword_negatives": TopicWakeWords,
}

// TopicOf is an event type's topic.
func TopicOf(eventType string) (Topic, bool) {
	t, ok := topicOf[eventType]
	return t, ok
}

// feedScopes are the scopes besides admin that allow some topic: a caller
// needs one of them, or admin, to open the feed.
func feedScopes() []auth.Scope {
	var out []auth.Scope
	for _, s := range auth.KnownScopes {
		for _, t := range Topics {
			if s != auth.ScopeAdmin && topicScopes[t] == s {
				out = append(out, s)
				break
			}
		}
	}
	return out
}
