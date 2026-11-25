package install

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestSSHPasswordArgsHaveNoKeyOrBatchMode(t *testing.T) {
	var calls []call
	s := &SSH{Target: "root@192.0.2.1", PasswordFile: "pw-file", KnownHosts: "/kh", exec: recorder(&calls, "\ncadence-rc=0\n", "", nil)}
	if _, err := s.Run(context.Background(), "true"); err != nil {
		t.Fatal(err)
	}
	args := calls[0].args
	for _, bad := range []string{"-i", "BatchMode=yes", "IdentitiesOnly=yes"} {
		if slices.Contains(args, bad) {
			t.Errorf("%s in %q", bad, args)
		}
	}
	for _, want := range []string{"PreferredAuthentications=password", "PubkeyAuthentication=no", "NumberOfPasswordPrompts=1",
		"StrictHostKeyChecking=yes", "UserKnownHostsFile=/kh", "ConnectTimeout=10"} {
		if !slices.Contains(args, want) {
			t.Errorf("%s missing from %q", want, args)
		}
	}
	for _, a := range args {
		if strings.Contains(a, "pw-file") {
			t.Errorf("password file named in argv: %q", a)
		}
	}
}

func TestSSHPasswordEnvPointsAskpassAtTheFile(t *testing.T) {
	var calls []call
	t.Setenv("SSH_ASKPASS", "parent")
	s := &SSH{Target: "root@192.0.2.1", PasswordFile: "/pw/file", exec: recorder(&calls, "\ncadence-rc=0\n", "", nil)}
	if _, err := s.Run(context.Background(), "true"); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	for _, want := range []string{"SSH_ASKPASS=" + self, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=:0", "CADENCE_INSTALL_ASKPASS=/pw/file"} {
		if !slices.Contains(calls[0].env, want) {
			t.Errorf("%s missing from %q", want, calls[0].env)
		}
	}
	if os.Getenv("SSH_ASKPASS") != "parent" || os.Getenv("CADENCE_INSTALL_ASKPASS") != "" {
		t.Error("the parent's environment changed")
	}
	key := &SSH{Target: "root@192.0.2.1", Key: "k", exec: recorder(&calls, "\ncadence-rc=0\n", "", nil)}
	_, _ = key.Run(context.Background(), "true")
	if calls[1].env != nil {
		t.Errorf("a key login got env %q", calls[1].env)
	}
}
