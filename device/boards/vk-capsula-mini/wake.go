package vkcapsula

import (
	"os"

	audiodv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/audiod/v1"
)

// «Маруся» on the stock model, which is read from the device and never shipped; audiod loads only the probed
// model's bytes. The policy is the offline gate's: the stock one with the pos threshold raised from 0.994, which
// woke on russian speech. On the shipped int8 path the gate's two hours peaked at 0.99826, and 0.997 with two calls
// in a row woke twice, so it is no fallback.
const (
	wakeModel       = "/usr/share/voice-agent/model.bin"
	wakeKind        = "vk-kws"
	wakeModelSHA256 = "8b33272b9fa8569f85630fd6208f475021ea806a7d79168e070a374569732f3b"
	wakePos         = 0.999
	wakeSP          = 0.2
	wakeRearm       = 147
	wakeConsecutive = 1
)

// keyword is the wake engine's section, nil without a stock model, so the talk key alone starts a turn
func (b *Board) keyword() *audiodv1.KeywordEngine {
	if st, err := os.Stat(b.paths.Model); err != nil || !st.Mode().IsRegular() {
		return nil
	}
	return &audiodv1.KeywordEngine{Kind: wakeKind, ModelPath: b.paths.Model, ModelSha256: wakeModelSHA256,
		PosThreshold: wakePos, SpThreshold: wakeSP, RearmFrames: wakeRearm, ConsecutiveCalls: wakeConsecutive}
}
