package screen_test

import (
	"testing"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/screen"
	"git.shino.boo/devilreef/cadence/device/internal/screen/screentest"
	screenv1 "git.shino.boo/devilreef/cadence/pkg/gen/cadence/screen/v1"
)

func TestClaimShownToEveryScreenAndCodesHandedOn(t *testing.T) {
	codes := make(chan string, 1)
	r := newRig(t, screen.Options{EnrollCode: func(c string) { codes <- c }})
	r.srv.ShowClaim(&screenv1.ClaimState{Phase: screenv1.ClaimPhase_CLAIM_PHASE_PENDING, Code: "K7M4QX"})
	// a screen that connects after the claim opened sees it, unenrolled
	s := r.connect(t, screentest.Options{})
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetLinkStatus().GetState() == screenv1.LinkState_LINK_STATE_UNENROLLED
	})
	frame(t, s, func(e *screenv1.Envelope) bool { return e.GetClaimState().GetCode() == "K7M4QX" })
	r.srv.ShowClaim(&screenv1.ClaimState{Phase: screenv1.ClaimPhase_CLAIM_PHASE_EXPIRED})
	frame(t, s, func(e *screenv1.Envelope) bool {
		return e.GetClaimState().GetPhase() == screenv1.ClaimPhase_CLAIM_PHASE_EXPIRED
	})
	if err := s.Send(&screenv1.Envelope{Body: &screenv1.Envelope_EnrollCode{EnrollCode: &screenv1.EnrollCode{Code: "ABCD-EFGH"}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-codes:
		if c != "ABCD-EFGH" {
			t.Fatalf("%q", c)
		}
	case <-time.After(wait):
		t.Fatal("no code")
	}
}
