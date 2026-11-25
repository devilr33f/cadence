package wifi

import (
	"context"
	"time"
)

// switchState is a selection in flight. it succeeds once the target holds the
// link with an address and the server link came up after that address; it
// rolls back when the address never comes within IPWithin of its start
// (no_ip), or the server link never comes within LinkWithin of the address
// (no_server: a captive or isolated network, a vlan with no route to the
// server, dns that fails).
type switchState struct {
	target, bssid string
	targetID      int // -1 when not known (a resumed switch whose network went)
	previousID    int // -1 when no network held the link
	previousSSID  string
	started       time.Time
	associated    time.Time // zero until the target held the link with an address
}

// pendingSwitch is a switch in flight as kept on disk: its target, and the
// network that held the link before it, by ssid (ids follow the supplicant's
// conf, which keeps its order) with the id as the first guess.
type pendingSwitch struct {
	SSID         string `json:"ssid"`
	BSSID        string `json:"bssid,omitempty"`
	TargetID     int    `json:"targetId"`
	PreviousSSID string `json:"previousSsid,omitempty"`
	PreviousID   int    `json:"previousId"`
}

// SetRedial takes core's way to make the link dial again at once.
func (c *Capability) SetRedial(redial func()) {
	if redial == nil {
		redial = func() {}
	}
	c.mu.Lock()
	c.redial = redial
	c.mu.Unlock()
}

// LinkChanged is core's word on the server link: up after each welcome, down
// once that link ended.
func (c *Capability) LinkChanged(up bool) {
	c.mu.Lock()
	c.linkUp = up
	if up {
		c.linkUpAt = time.Now()
	}
	c.mu.Unlock()
	c.wake()
}

// resumeLocked picks up a switch a restart cut short: its clock starts over
// at boot, and the server link must come up over the target again, so a
// device that came back on a network that cannot reach the server still
// rolls back; callers hold mu (or own c alone).
func (c *Capability) resumeLocked(now time.Time) {
	p := c.kept.Pending
	if p == nil || p.SSID == "" {
		c.kept.Pending = nil
		return
	}
	c.sw = &switchState{target: p.SSID, bssid: p.BSSID, targetID: p.TargetID,
		previousID: p.PreviousID, previousSSID: p.PreviousSSID, started: now}
	c.log.Info("wifi_switch_resumed", "capability", CapabilityID)
}

// beginSwitchLocked starts the clock of a switch whose target is selected
// (or already holds the link) and keeps it on disk; callers hold mu.
func (c *Capability) beginSwitchLocked(target, bssid string, targetID, prevID int, prevSSID string) {
	c.sw = &switchState{target: target, bssid: bssid, targetID: targetID,
		previousID: prevID, previousSSID: prevSSID, started: time.Now()}
	c.persistSwitchLocked()
	c.log.Info("wifi_switch_started", "capability", CapabilityID, "target_id", targetID, "previous_id", prevID)
	c.setLocked(c.stateFor(c.st))
	c.wake()
}

func (c *Capability) persistSwitchLocked() {
	sw := c.sw
	c.kept.Pending = &pendingSwitch{SSID: sw.target, BSSID: sw.bssid, TargetID: sw.targetID,
		PreviousSSID: sw.previousSSID, PreviousID: sw.previousID}
	c.save()
}

// watch reports the link, finishes switches, rolls failed ones back, and once
// after settling re-selects the managed network when a boot fell off it (the
// router it wants may have come back while the device booted beside it).
func (c *Capability) watch(ctx context.Context, boot time.Time) {
	defer close(c.done)
	tick := time.NewTicker(c.t.Poll)
	defer tick.Stop()
	settled := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.kick:
		case <-tick.C:
		}
		c.mu.Lock()
		if !settled && time.Since(boot) >= c.t.Settle {
			settled = true
			c.resettleLocked(ctx)
		}
		c.pollLocked(ctx)
		c.mu.Unlock()
	}
}

// resettleLocked selects the kept network again when a boot's association
// landed elsewhere; callers hold mu.
func (c *Capability) resettleLocked(ctx context.Context) {
	kept := c.kept
	if !kept.Enabled || kept.SSID == "" || kept.SSID == kept.RolledBack || c.sw != nil {
		return
	}
	be, err := c.backend(ctx)
	if err != nil {
		return
	}
	st, err := be.Status(ctx)
	if err != nil || onTarget(st, kept.SSID, kept.BSSID) {
		return
	}
	saved, err := be.List(ctx)
	if err != nil {
		return
	}
	for _, n := range saved {
		if n.SSID != kept.SSID {
			continue
		}
		if kept.BSSID != "" && n.BSSID != kept.BSSID {
			continue
		}
		if err := be.Select(ctx, n.ID); err == nil {
			c.log.Info("wifi_resettle", "capability", CapabilityID)
			c.beginSwitchLocked(kept.SSID, kept.BSSID, n.ID, st.CurrentID, st.SSID)
		}
		return
	}
}

// pollLocked reads the link once, closes or rolls back the switch in flight,
// and offers the state; callers hold mu.
func (c *Capability) pollLocked(ctx context.Context) {
	be, err := c.backend(ctx)
	if err != nil {
		c.setLocked(state{State: "unsupported", Managed: c.kept.Enabled, Code: c.cur.Code})
		return
	}
	st, err := be.Status(ctx)
	if err != nil {
		c.setLocked(state{State: StateUnknown, Managed: c.kept.Enabled, Code: "wpa"})
		return
	}
	c.st = st
	now := time.Now()
	if sw := c.sw; sw != nil {
		on := onTarget(st, sw.target, sw.bssid)
		if on && sw.associated.IsZero() {
			// whatever the link holds or dials went over the old network:
			// it dials again now, over this one
			sw.associated = now
			c.log.Info("wifi_associated", "capability", CapabilityID, "after_ms", now.Sub(sw.started).Milliseconds())
			c.redial()
		}
		switch {
		case on && c.linkUp && !c.linkUpAt.Before(sw.associated):
			c.log.Info("wifi_switched", "capability", CapabilityID, "after_ms", now.Sub(sw.started).Milliseconds())
			c.sw = nil
			c.kept.Pending = nil
			c.save()
		case sw.associated.IsZero() && now.Sub(sw.started) >= c.t.IPWithin:
			c.rollbackLocked(ctx, be, reasonNoIP)
		case !sw.associated.IsZero() && now.Sub(sw.associated) >= c.t.LinkWithin:
			c.rollbackLocked(ctx, be, reasonNoServer)
		}
	} else if c.rejoin != "" && st.State == StateCompleted && st.IP != "" && st.SSID != c.rejoin {
		// back on the previous network: dial now rather than after the
		// link's backoff
		c.rejoin = ""
		if !c.linkUp {
			c.redial()
		}
	}
	c.setLocked(c.stateFor(st))
}

// rollbackLocked selects the previous network back, disables the target (kept,
// never removed, so the supplicant does not return to it on its own) and keeps
// the ssid it gave up on with the generation in force; callers hold mu.
func (c *Capability) rollbackLocked(ctx context.Context, be Backend, reason string) {
	sw := c.sw
	prev := c.previousLocked(ctx, be, sw)
	if prev >= 0 {
		if err := be.Select(ctx, prev); err != nil {
			c.log.Warn("wifi_rollback_failed", "capability", CapabilityID, "code", "wpa", "step", "select")
		}
	}
	if sw.targetID >= 0 && sw.targetID != prev {
		if err := be.Disable(ctx, sw.targetID); err != nil {
			c.log.Warn("wifi_rollback_failed", "capability", CapabilityID, "code", "wpa", "step", "disable")
		}
	}
	c.sw = nil
	c.kept.Pending = nil
	c.kept.RolledBack, c.kept.RolledBackGen, c.kept.RolledBackReason = sw.target, c.gen, reason
	c.save()
	c.rejoin = sw.target
	c.redial()
	c.log.Warn("wifi_rolled_back", "capability", CapabilityID, "reason", reason, "previous_id", prev,
		"after_ms", time.Since(sw.started).Milliseconds())
}

// previousLocked finds the network to roll back to: the id kept, when the
// supplicant still holds that ssid there, else the ssid's id now.
func (c *Capability) previousLocked(ctx context.Context, be Backend, sw *switchState) int {
	if sw.previousSSID == "" || sw.previousSSID == sw.target {
		return sw.previousID
	}
	saved, err := be.List(ctx)
	if err != nil {
		return sw.previousID
	}
	for _, n := range saved {
		if n.ID == sw.previousID && n.SSID == sw.previousSSID {
			return n.ID
		}
	}
	for _, n := range saved {
		if n.SSID == sw.previousSSID {
			return n.ID
		}
	}
	return sw.previousID
}
