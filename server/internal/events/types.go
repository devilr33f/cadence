package events

import "time"

// MediaState carries a device's media session, metadata included: it reaches
// the feeds of the media topic only (admin and ha callers), is kept in memory
// and never logged.
const MediaState = "media_state"

// ToolCard is a tool call's line and card at its latest state, as the turn's screens show it
// (screens.ToolCard): the dashboard replaces the call's earlier one. Admin's agent topic alone; never logged.
const ToolCard = "tool_card"

// AssistantSpeech says how a dashboard assistant turn's reply went to its
// device's speaker: {code}, "ok" once it plays, never the text. Admin's
// sessions topic alone.
const AssistantSpeech = "assistant_speech"

// MemoryChanged says an admin created, updated, deleted, approved or
// rejected a memory: {id, action}, never its text.
const MemoryChanged = "memory_changed"

// TaskEvent says a background task changed: {short_id, kind, status, seq},
// never its text. kind is the event kind (progress, waiting_input, done,
// failed, cancelled, unknown, or answered: an approval was answered).
const TaskEvent = "task_event"

// The subscriptions topic's events (the subscriptions hub): SubscriptionUpdate
// an update stored for a device {id, topic, event, state}; SubscriptionChanged
// a subscription created, deleted or ended by its next stop {id, topic,
// action}; SubscriptionInbox a device's inbox changing {ids, action}: held,
// delivered, inbox, seen or answered. Never an outside text.
const (
	SubscriptionUpdate  = "subscription_update"
	SubscriptionChanged = "subscription_changed"
	SubscriptionInbox   = "subscription_inbox"
)

// MCPConnected says an MCP server listed its tools on a session, first or
// again after a failure: {instance, era, tools}, the tools a count.
const MCPConnected = "mcp_connected"

// MCPUnavailable says an MCP server failed, or failed with another code:
// {instance, code}.
const MCPUnavailable = "mcp_unavailable"

// MCPToolsChanged says an MCP server's tool counts changed: {instance, total,
// offered, changed, skipped}, never a tool's name or text.
const MCPToolsChanged = "mcp_tools_changed"

// LogLine is one stored log line, a device's or the server's: {deviceId?,
// source, severity, ts, body}. Third-party bodies ship as written (scrubbed of
// known secrets), so like a transcript it is on the logs topic, which only an
// admin feed that subscribed to it reads (Ruling M7a-R5), at most 50 a second
// each; the rest are counted in one LogLineDropped frame {count} a second.
const LogLine = "log_line"

// LogLineDropped is the feed's own frame for the log lines a peer missed.
const LogLineDropped = "log_line_dropped"

// The devices topic's events (M8-D2), each about one device, by its id:
// DeviceOnline and DeviceOffline as its link opens and closes, DeviceAdded
// once its enrolment completed, DeviceRenamed after a rename, each with a
// DeviceData; DeviceDeleted once its delete committed, with no data; and
// DeviceVolume with a VolumeData whenever its reported audio.output level
// changed (Ruling M8-R1). A device's name is the owner's, never a transcript.
const (
	DeviceOnline  = "device_online"
	DeviceOffline = "device_offline"
	DeviceAdded   = "device_added"
	DeviceRenamed = "device_renamed"
	DeviceDeleted = "device_deleted"
	DeviceVolume  = "device_volume"
)

// DeviceVitals is the vitals topic's event (M8-D3): a device's cpu, memory,
// disks and temperature from one stored system.metrics round, at most one a
// device each 30 s; never a process.
const DeviceVitals = "device_vitals"

// BusyChanged is the presence topic's event: a device's busy state changed
// (busy mode), {busy, reason?}; never a signal's source.
const BusyChanged = "busy_changed"

// DeviceData is a presence event's data: the device as the device list shows
// it, online whether it is linked now.
type DeviceData struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	Online bool   `json:"online"`
}

// VolumeData is a DeviceVolume's data: the level the device reported, 0–100.
type VolumeData struct {
	Level int `json:"level"`
}

// The groups topic's events (multiroom playback): GroupChanged after a group's membership or config changed, or its
// elected leader did, with a GroupData (once deleted, its id alone and deleted); GroupState whenever a member's
// reported media.group state changed, by the reporting device, with a GroupStateData. Ids, the owner's names and
// numbers only (rule 8).
const (
	GroupChanged = "group_changed"
	GroupState   = "group_state"
)

// GroupData is a group_changed's data: the group as list-groups shows it, leaderId "" while none is elected.
type GroupData struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	LeaderID string   `json:"leaderId"`
	Members  []string `json:"members"`
	Deleted  bool     `json:"deleted,omitempty"`
}

// GroupStateData is a group_state's data: the group the server has the device in, and the device's media.group state
// as it reported it (capschema.MediaGroupState: numbers and tokens).
type GroupStateData struct {
	GroupID string `json:"groupId"`
	Status  any    `json:"status"`
}

// The alarms topic's events, each about one device by its id (alarmview): AlarmsState whenever its reported alarms
// state changed, with the state as the device reported it (alarms, timers, reminders with their labels, ringing,
// snoozed, lastRing; capability alarms); AlarmRinging once a ring begins, with an AlarmRingData; AlarmRingEnded once it
// stops, with an AlarmRingEndedData. A reminder's label is the owner's setting, shown on screens and in Home Assistant
// (owner, 2026-11-16), never logged.
const (
	AlarmsState    = "alarms_state"
	AlarmRinging   = "alarm_ringing"
	AlarmRingEnded = "alarm_ring_ended"
)

// AlarmRingData is an alarm_ringing's data: the ring's kind (alarm, timer, reminder) and id, a reminder's label, an
// alarm's HH:MM, and when the server first heard it ring.
type AlarmRingData struct {
	Kind      string    `json:"kind"`
	ID        string    `json:"id"`
	Label     string    `json:"label,omitempty"`
	Time      string    `json:"time,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

// AlarmRingEndedData is an alarm_ring_ended's data: the ring, how it ended (stopped, snoozed: put off until
// snoozedUntil, or missed: it rang to its limit unanswered), the device's stop reason (button, volume, wake, word,
// timeout, snooze, remote; "" when the device did not say) and when.
type AlarmRingEndedData struct {
	Kind         string     `json:"kind"`
	ID           string     `json:"id"`
	Outcome      string     `json:"outcome"`
	StoppedBy    string     `json:"stoppedBy,omitempty"`
	SnoozedUntil *time.Time `json:"snoozedUntil,omitempty"`
	At           time.Time  `json:"at"`
}

// The traces topic's events (internal/tracing.FeedData {seq, span}): a span
// begun, a live span's byte counters (at most one a second a span) and a span
// ended or arrived ended from a device. Each carries the whole span, an upsert
// by its spanId; ids, names, codes, numbers and timings only (rule 8). A feed
// gets at most spanRate a second, the rest counted in one SpanDropped frame
// {count} a second, after which a reader asks list-traces for the traces
// changed since the last seq it saw.
const (
	SpanStart   = "span_start"
	SpanUpdate  = "span_update"
	SpanEnd     = "span_end"
	SpanDropped = "span_dropped"
)
