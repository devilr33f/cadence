package install

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SSH runs commands over ssh with a key, against the dropbear provisioning installed. The host key must already be
// in KnownHosts: an unknown or changed key fails, never trusted on first use.
type SSH struct {
	// Target is user@host
	Target string
	Key    string
	// PasswordFile holds the password on its first line, for a dropbear without public-key auth. ssh reads it
	// through SSH_ASKPASS, which runs this program in askpass mode: the password is never on an argument
	PasswordFile string
	KnownHosts   string
	// Bin is the ssh program, "ssh" when empty
	Bin  string
	exec execFunc
}

// DefaultKnownHosts is ~/.ssh/known_hosts.
func DefaultKnownHosts() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "known_hosts")
}

func (s *SSH) String() string { return "ssh " + s.Target }

func (s *SSH) Run(ctx context.Context, script string) (Result, error) {
	return s.run(ctx, wrap(script), nil)
}

// Push streams the file into cat on the device: the speaker has no scp or sftp
func (s *SSH) Push(ctx context.Context, local, path string) error {
	f, err := os.Open(local) //nolint:gosec // the installer's own file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	res, err := s.run(ctx, wrap("cat > "+quote(path)), f)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s: writing %s failed (status %d)", s, path, res.Code)
	}
	return nil
}

func (s *SSH) run(ctx context.Context, command string, stdin io.Reader) (Result, error) {
	run := s.exec
	if run == nil {
		run = hostExec
	}
	bin := s.Bin
	if bin == "" {
		bin = "ssh"
	}
	out, errOut, err := run(ctx, bin, s.args(command), s.env(), stdin)
	if res, ok := unwrap(out); ok {
		return res, nil
	}
	return Result{}, transportError(s.String(), errOut, err)
}

// args never allocate a terminal, which would mangle pushed bytes, and never prompt or trust a new host key
func (s *SSH) args(command string) []string {
	auth := []string{"-i", s.Key, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes"}
	if s.PasswordFile != "" {
		auth = []string{"-o", "PreferredAuthentications=password", "-o", "PubkeyAuthentication=no", "-o", "NumberOfPasswordPrompts=1"}
	}
	return append(append([]string{"-T"}, auth...), []string{
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + sshPath(s.KnownHosts),
		"-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4",
		"--", s.Target, command,
	}...)
}

// AskpassEnv names the file cadence-install prints the first line of, and exits, when ssh runs it as its askpass
const AskpassEnv = "CADENCE_INSTALL_ASKPASS" //nolint:gosec // an env var's name, not a credential

// env goes to ssh alone: the installer re-executes itself as ssh's askpass program, which windows needs to be an
// executable
func (s *SSH) env() []string {
	if s.PasswordFile == "" {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	return []string{"SSH_ASKPASS=" + self, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=:0", AskpassEnv + "=" + s.PasswordFile}
}

// sshPath is a path as ssh's option parser reads it: forward slashes, which windows' ssh takes too, and quoted when
// it holds a space
func sshPath(p string) string {
	p = filepath.ToSlash(p)
	if strings.ContainsAny(p, " \t") {
		return `"` + p + `"`
	}
	return p
}
