package groups

import (
	"context"

	"git.shino.boo/devilreef/cadence/pkg/capschema"
)

// calibrationOutputs snapshots what each member played through when the run was requested. The run's trims keep these
// ids even if a route changes before the result arrives.
func (s *Service) calibrationOutputs(ctx context.Context, g Group) map[string]capschema.MediaGroupOutput {
	out := map[string]capschema.MediaGroupOutput{}
	for _, m := range g.Members {
		st, ok := s.Status(ctx, m.DeviceID)
		if !ok || st.Output == nil || len(st.Output.ID) == 0 || len(st.Output.ID) > 64 {
			continue
		}
		o := *st.Output
		switch o.Kind {
		case capschema.GroupOutputSpeaker, capschema.GroupOutputHDMI, capschema.GroupOutputBluetooth, capschema.GroupOutputOther:
		default:
			continue
		}
		if o.DelayHintUs != nil && (*o.DelayHintUs < 0 || *o.DelayHintUs > 60_000_000) {
			o.DelayHintUs = nil
		}
		out[m.DeviceID] = o
	}
	return out
}

// calibrationPriors give the leader a search window around each output's expected latency, us.
func calibrationPriors(g Group, outputs map[string]capschema.MediaGroupOutput) map[string]capschema.MediaGroupCalibratePrior {
	if len(outputs) == 0 {
		// a group of devices from before output reports keeps the leader's legacy window
		return nil
	}
	out := make(map[string]capschema.MediaGroupCalibratePrior, len(g.Members))
	for _, m := range g.Members {
		o, ok := outputs[m.DeviceID]
		prior := capschema.MediaGroupCalibratePrior{PriorSpanUs: 250_000}
		if ok {
			switch o.Kind {
			case capschema.GroupOutputSpeaker:
				prior.PriorSpanUs = 150_000
			case capschema.GroupOutputHDMI:
				prior.PriorUs, prior.PriorSpanUs = 80_000, 200_000
			case capschema.GroupOutputBluetooth:
				prior.PriorUs = 200_000
				if o.DelayHintUs != nil {
					prior.PriorUs, prior.PriorSpanUs = int64(*o.DelayHintUs), 100_000
				}
			}
		}
		out[m.DeviceID] = prior
	}
	return out
}

// measuredTrim is the active output's stored trim, or the member's fallback when that output was never calibrated.
func measuredTrim(m Member, outputs map[string]capschema.MediaGroupOutput) int {
	if o, ok := outputs[m.DeviceID]; ok {
		for _, t := range m.Trims {
			if t.OutputID == o.ID {
				return t.TrimUs
			}
		}
	}
	return m.TrimUs
}

func measuredOutput(outputs map[string]capschema.MediaGroupOutput, deviceID string) *capschema.MediaGroupOutput {
	o, ok := outputs[deviceID]
	if !ok {
		return nil
	}
	out := capschema.MediaGroupOutput{ID: o.ID, Kind: o.Kind}
	if o.DelayHintUs != nil {
		hint := *o.DelayHintUs
		out.DelayHintUs = &hint
	}
	return &out
}
