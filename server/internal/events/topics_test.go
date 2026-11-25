package events

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/auth"
)

// every event type the code publishes has exactly one topic (M7a-D7): the
// literal types found in the sources that publish, and the constants here
var published = regexp.MustCompile(`(?:publish\((?:tr, )?|Type:\s*)"([a-z_]+)"`)

func TestEveryPublishedTypeHasATopic(t *testing.T) {
	found := map[string]string{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// a file that publishes: it builds an event or calls a publish helper
		if !strings.Contains(string(src), "publish(") && !strings.Contains(string(src), "events.Event{") {
			return nil
		}
		for _, m := range published.FindAllStringSubmatch(string(src), -1) {
			found[m[1]] = path
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{MediaState, AssistantSpeech, MemoryChanged, TaskEvent, MCPConnected, MCPUnavailable, MCPToolsChanged, LogLine,
		DeviceOnline, DeviceOffline, DeviceAdded, DeviceRenamed, DeviceDeleted, DeviceVolume, DeviceVitals, BusyChanged, GroupChanged, GroupState} {
		found[typ] = "types.go"
	}
	if len(found) < 20 {
		t.Fatalf("the scan found only %v", found)
	}
	for typ, path := range found {
		if _, ok := TopicOf(typ); !ok {
			t.Errorf("%s (%s) has no topic", typ, path)
		}
	}
}

// haTypes are exactly the event types an ha feed reads (M7a-D7, M8-D2, M8-D3):
// presence, volume, media, vitals and alarms (a reminder's label is the
// owner's setting, owner 2026-11-16), none with a transcript, tool or task
// text. Widening it is a deliberate diff here.
var haTypes = []string{
	AlarmRingEnded, AlarmRinging, AlarmsState, DeviceAdded, DeviceDeleted, DeviceOffline, DeviceOnline, DeviceRenamed, DeviceVitals, DeviceVolume, GroupChanged,
	GroupState, MediaState,
}

// the topics, and which a scope may subscribe to: ha gets devices, media and
// vitals, metrics none, admin all
func TestTopicScopes(t *testing.T) {
	want := []Topic{TopicDevices, TopicSessions, TopicAgent, TopicTasks, TopicMedia, TopicVitals, TopicLogs, TopicPresence, TopicSubscriptions, TopicGroups, TopicAlarms, TopicTraces, TopicOps, TopicWakeWords}
	if !slices.Equal(Topics, want) {
		t.Fatalf("topics %v", Topics)
	}
	for _, typ := range slices.Sorted(func(yield func(string) bool) {
		for k := range topicOf {
			if !yield(k) {
				return
			}
		}
	}) {
		if !slices.Contains(Topics, topicOf[typ]) {
			t.Errorf("%s: topic %q", typ, topicOf[typ])
		}
	}
	allowed := func(s auth.Scopes) []Topic {
		var out []Topic
		for _, topic := range Topics {
			if topic.allowed(s) {
				out = append(out, topic)
			}
		}
		return out
	}
	if got := allowed(auth.Scopes{auth.ScopeHA}); !slices.Equal(got, []Topic{TopicDevices, TopicMedia, TopicVitals, TopicGroups, TopicAlarms}) {
		t.Errorf("ha: %v", got)
	}
	if got := allowed(auth.Scopes{auth.ScopeMetrics}); len(got) != 0 {
		t.Errorf("metrics: %v", got)
	}
	if got := allowed(auth.AdminScopes); !slices.Equal(got, Topics) {
		t.Errorf("admin: %v", got)
	}
	// what ha may read never carries text: no transcript, tool or task reaches it
	var read []string
	for typ, topic := range topicOf {
		if topic.allowed(auth.Scopes{auth.ScopeHA}) {
			read = append(read, typ)
		}
	}
	slices.Sort(read)
	if !slices.Equal(read, haTypes) {
		t.Errorf("ha reads %v, want %v", read, haTypes)
	}
}
