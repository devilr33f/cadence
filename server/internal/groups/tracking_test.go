package groups_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
	"git.shino.boo/devilreef/cadence/server/internal/events"
	"git.shino.boo/devilreef/cadence/server/internal/groups"
)

func withTracking(body string) string {
	return strings.Replace(body, `"members":`, `"trackingEnabled":true,"members":`, 1)
}

func TestTrackingEnabledDefaultsOffAndRoundTrips(t *testing.T) {
	r := newRig(t, 2)
	r.link(0, "10.0.0.10")
	r.link(1, "10.0.0.11")
	created := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", r.groupBody("Home", 0, 1)))
	if created.TrackingEnabled || created.Tracking.State != "off" || created.Tracking.Members == nil {
		t.Fatalf("default tracking = %+v %+v", created.TrackingEnabled, created.Tracking)
	}
	lead, _ := r.devs.of(r.ids[0])
	if lead.Tracking == nil || lead.Tracking.Enabled {
		t.Fatalf("default config tracking = %+v", lead.Tracking)
	}

	updated := decode[groups.GroupView](t, r.must(200, http.MethodPut, "/groups/"+created.ID, withTracking(r.groupBody("Home", 0, 1))))
	read := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+created.ID, ""))
	if !updated.TrackingEnabled || !read.TrackingEnabled {
		t.Fatalf("tracking did not round-trip: %+v %+v", updated.TrackingEnabled, read.TrackingEnabled)
	}
	lead, _ = r.devs.of(r.ids[0])
	if lead.Tracking == nil || lead.Tracking.Enabled || len(lead.Tracking.Members) != 0 {
		t.Fatalf("uncalibrated config tracking = %+v", lead.Tracking)
	}
}

func TestTrackingConfigIncludesOnlyUltrasonicOKActiveOutputs(t *testing.T) {
	r := newRig(t, 3)
	for i := range 3 {
		r.link(i, fmt.Sprintf("10.0.0.%d", i+10))
		outputState(r, i, capschema.GroupOutputSpeaker, nil)
	}
	g := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", withTracking(r.groupBody("Home", 0, 1, 2))))
	r.cmds.answer = result("ok",
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[0], Code: "ok", Ultrasonic: "ok"},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[1], Code: "ok", Ultrasonic: "weak"},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[2], Code: "ok", Ultrasonic: "ok"})
	calibrated := decode[groups.CalibrationView](t, r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", ""))
	for i, want := range []string{"ok", "weak", "ok"} {
		if calibrated.Members[i].Ultrasonic != want {
			t.Errorf("calibration member %d ultrasonic = %q", i, calibrated.Members[i].Ultrasonic)
		}
	}
	lead, _ := r.devs.of(r.ids[0])
	if lead.Tracking == nil || !lead.Tracking.Enabled || len(lead.Tracking.Members) != 2 ||
		lead.Tracking.Members[0] != r.ids[0] || lead.Tracking.Members[1] != r.ids[2] {
		t.Fatalf("leader tracking = %+v", lead.Tracking)
	}
	member, _ := r.devs.of(r.ids[1])
	if member.Tracking == nil || !member.Tracking.Enabled || len(member.Tracking.Members) != 0 {
		t.Fatalf("member tracking = %+v", member.Tracking)
	}
	view := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+g.ID, ""))
	for i, want := range []string{"ok", "weak", "ok"} {
		if len(view.Members[i].Trims) != 1 || view.Members[i].Trims[0].Ultrasonic != want {
			t.Errorf("stored member %d trims = %+v", i, view.Members[i].Trims)
		}
	}

	// the opt-in switch is a whole-group update: retained members keep their per-output calibration rows
	r.must(200, http.MethodPut, "/groups/"+g.ID, withTracking(r.groupBody("Home", 0, 1, 2)))
	view = decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+g.ID, ""))
	lead, _ = r.devs.of(r.ids[0])
	if len(view.Members[0].Trims) != 1 || view.Members[0].Trims[0].Ultrasonic != "ok" ||
		lead.Tracking == nil || !lead.Tracking.Enabled || len(lead.Tracking.Members) != 2 {
		t.Fatalf("update lost ultrasonic eligibility: trims %+v, config %+v", view.Members[0].Trims, lead.Tracking)
	}

	// an output never proven for ultrasound leaves too few members to time against one another
	outputState(r, 2, capschema.GroupOutputOther, nil)
	r.svc.Recompute(t.Context())
	lead, _ = r.devs.of(r.ids[0])
	if lead.Tracking == nil || lead.Tracking.Enabled || len(lead.Tracking.Members) != 0 {
		t.Fatalf("one capable member left tracking on: %+v", lead.Tracking)
	}
}

func trackedCorrectionRig(t *testing.T, firstOffset int) (*rig, groups.GroupView) {
	t.Helper()
	r := newRig(t, 2)
	for i := range 2 {
		r.link(i, fmt.Sprintf("10.0.0.%d", i+10))
		outputState(r, i, capschema.GroupOutputSpeaker, nil)
	}
	g := decode[groups.GroupView](t, r.must(201, http.MethodPost, "/groups", withTracking(r.groupBody("Home", 0, 1))))
	r.cmds.answer = result("ok",
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[0], Code: "ok", OffsetUs: firstOffset, Ultrasonic: "ok"},
		capschema.MediaGroupCalibrationMember{DeviceID: r.ids[1], Code: "ok", Ultrasonic: "ok"})
	r.must(200, http.MethodPost, "/groups/"+g.ID+"/calibrate", "")
	return r, g
}

func reportTracking(r *rig, g groups.GroupView, correctionID uint32, correction int) {
	st := capschema.MediaGroupStatus{GroupID: g.ID, Role: capschema.GroupRoleLeader, State: "playing",
		Output: &capschema.MediaGroupOutput{ID: "speaker", Kind: capschema.GroupOutputSpeaker},
		Tracking: capschema.MediaGroupTracking{State: "tracking", Members: []capschema.MediaGroupTrackingMember{
			{DeviceID: r.ids[0], OffsetUs: 1926, Ci95Us: 40, Cycles: 6, CorrectionUs: correction, CorrectionID: correctionID},
			{DeviceID: r.ids[1], OffsetUs: -1926, Ci95Us: 40, Cycles: 6},
		}}}
	raw, err := json.Marshal(st)
	if err != nil {
		r.t.Fatal(err)
	}
	r.devs.state[r.ids[0]] = raw
	r.svc.StateChanged(r.ids[0], capschema.MediaGroupID, raw)
}

func pauseTracking(r *rig, g groups.GroupView) {
	st := capschema.MediaGroupStatus{GroupID: g.ID, Role: capschema.GroupRoleLeader, State: "idle",
		Tracking: capschema.MediaGroupTracking{State: "idle", Members: []capschema.MediaGroupTrackingMember{}}}
	raw, err := json.Marshal(st)
	if err != nil {
		r.t.Fatal(err)
	}
	r.svc.StateChanged(r.ids[0], capschema.MediaGroupID, raw)
}

func waitForTrim(t *testing.T, r *rig, groupID string, want int) groups.GroupView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+groupID, ""))
		if len(view.Members[0].Trims) == 1 && view.Members[0].Trims[0].TrimUs == want {
			return view
		}
		time.Sleep(10 * time.Millisecond)
	}
	view := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+groupID, ""))
	t.Fatalf("trim = %+v, want %d; logs %s", view.Members[0].Trims, want, r.logs.String())
	return groups.GroupView{}
}

func TestTrackingCorrectionAppliesOnceAndIsRateLimited(t *testing.T) {
	r, g := trackedCorrectionRig(t, 0)
	feed, stop := r.bus.Subscribe(nil)
	defer stop()
	reportTracking(r, g, 1, -1200)
	view := waitForTrim(t, r, g.ID, -1200)
	tracked := view.Tracking.Members[0]
	if view.Tracking.State != "tracking" || tracked.OffsetUs != 1926 || tracked.Ci95Us != 40 || tracked.Cycles != 6 ||
		tracked.LastCorrectionUs != -1200 || tracked.LastCorrectionAt == nil || !tracked.LastCorrectionAt.Equal(r.now) {
		t.Fatalf("tracking view = %+v", view.Tracking)
	}
	lead, _ := r.devs.of(r.ids[0])
	if len(lead.Trims) != 1 || lead.Trims[0].TrimUs != -1200 {
		t.Fatalf("pushed trims = %+v", lead.Trims)
	}
	if event := <-feed; event.Type != events.GroupState {
		t.Fatalf("feed event = %+v", event)
	}

	// a replay stays a no-op even after both rate limits would have cooled
	sets := r.devs.count()
	r.advance(61 * time.Second)
	reportTracking(r, g, 1, -1200)
	time.Sleep(50 * time.Millisecond)
	view = decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+g.ID, ""))
	if view.Members[0].Trims[0].TrimUs != -1200 || r.devs.count() != sets {
		t.Fatalf("replay changed trim or config: %+v, %d -> %d", view.Members[0].Trims, sets, r.devs.count())
	}

	// an intervening idle report lets a restarted audiod's counter begin at one again
	pauseTracking(r, g)
	reportTracking(r, g, 1, -100)
	waitForTrim(t, r, g.ID, -1300)
}

func TestTrackingCorrectionRateLimitAndTrimBound(t *testing.T) {
	r, g := trackedCorrectionRig(t, 0)
	reportTracking(r, g, 1, -1200)
	waitForTrim(t, r, g.ID, -1200)
	r.advance(30 * time.Second)
	reportTracking(r, g, 2, -100)
	time.Sleep(50 * time.Millisecond)
	view := decode[groups.GroupView](t, r.must(200, http.MethodGet, "/groups/"+g.ID, ""))
	if view.Members[0].Trims[0].TrimUs != -1200 {
		t.Fatalf("correction inside 60 s applied: %+v", view.Members[0].Trims)
	}
	r.advance(31 * time.Second)
	reportTracking(r, g, 3, -100)
	view = waitForTrim(t, r, g.ID, -1300)
	if view.Tracking.Members[0].LastCorrectionUs != -100 {
		t.Fatalf("last correction = %+v", view.Tracking.Members[0])
	}

	bounded, bg := trackedCorrectionRig(t, -499900)
	reportTracking(bounded, bg, 1, 200)
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(bounded.logs.String(), `"code":"out_of_range"`) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	view = decode[groups.GroupView](t, bounded.must(200, http.MethodGet, "/groups/"+bg.ID, ""))
	if view.Members[0].Trims[0].TrimUs != 499900 || !strings.Contains(bounded.logs.String(), `"group_tracking_correction"`) ||
		!strings.Contains(bounded.logs.String(), `"correction_us":200`) || !strings.Contains(bounded.logs.String(), `"trim_us":499900`) {
		t.Fatalf("out-of-range correction: trims %+v, logs %s", view.Members[0].Trims, bounded.logs.String())
	}
}
