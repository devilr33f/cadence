package deviceops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"git.shino.boo/devilreef/cadence/server/internal/secrets"
	"git.shino.boo/devilreef/cadence/server/internal/store/db"
)

// sshTransport drives the pi over ssh: each command one session, files over sftp. The private key
// is the device's access row, sealed with the master key; the host key is pinned on the first
// connection (trust on first use, the dashboard shows it), and a later change refuses the
// transport instead of connecting on.
type sshTransport struct {
	box *secrets.Box
	q   queries
}

// sshDial dials and answers the client plus the host key the handshake showed.
func (t *sshTransport) dial(ctx context.Context, acc access) (*ssh.Client, string, error) {
	if acc.addr == "" {
		return nil, "", ErrNoTransport
	}
	rest := strings.TrimPrefix(acc.addr, "ssh://")
	user := "root"
	hostPort := rest
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		user, hostPort = rest[:at], rest[at+1:]
	}
	if _, _, err := net.SplitHostPort(hostPort); err != nil {
		hostPort = hostPort + ":22"
	}
	if acc.sshKey == "" || t.box == nil {
		return nil, "", fmt.Errorf("%w: no ssh key", ErrBadAccess)
	}
	plain, err := t.box.Open([]byte(acc.sshKey), accessAAD(acc.deviceID))
	if err != nil {
		return nil, "", fmt.Errorf("%w: key", ErrBadAccess)
	}
	signer, err := ssh.ParsePrivateKey(plain)
	if err != nil {
		return nil, "", fmt.Errorf("%w: key", ErrBadAccess)
	}
	var got string
	cb := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got = string(ssh.MarshalAuthorizedKey(key))
		if acc.sshHostKey != "" {
			if normalizeHostKey(got) != normalizeHostKey(acc.sshHostKey) {
				return fmt.Errorf("deviceops: host key changed")
			}
			return nil
		}
		// first use: take it (the run's log names the pin for the owner to check)
		return nil
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: cb,
		Timeout:         15 * time.Second,
	}
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, "", fmt.Errorf("deviceops: ssh dial: %w", err)
	}
	sshc, chans, reqs, err := ssh.NewClientConn(conn, hostPort, cfg)
	if err != nil {
		return nil, "", fmt.Errorf("deviceops: ssh: %w", err)
	}
	client := ssh.NewClient(sshc, chans, reqs)
	if acc.sshHostKey == "" && got != "" && t.q != nil {
		uid, ok := uuid(acc.deviceID)
		if ok {
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = t.q.SetDeviceAccessHostKey(cctx, db.SetDeviceAccessHostKeyParams{DeviceID: uid, HostKey: strings.TrimSpace(got)})
		}
	}
	return client, got, nil
}

func normalizeHostKey(line string) string {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) < 2 {
		return strings.TrimSpace(line)
	}
	return f[0] + " " + f[1]
}

func (t *sshTransport) probe(ctx context.Context, acc access) error {
	c, _, err := t.dial(ctx, acc)
	if err != nil {
		return err
	}
	return c.Close()
}

func (t *sshTransport) exec(ctx context.Context, acc access, cmd string, wait time.Duration) (int, string, error) {
	c, _, err := t.dial(ctx, acc)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = c.Close() }()
	if wait <= 0 {
		wait = execDefaultWait
	}
	sctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	sess, err := c.NewSession()
	if err != nil {
		return 0, "", fmt.Errorf("deviceops: ssh session: %w", err)
	}
	defer func() { _ = sess.Close() }()
	done := make(chan struct{})
	var out bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &out
	errc := make(chan error, 1)
	go func() { errc <- sess.Run(cmd) }()
	select {
	case err := <-errc:
		close(done)
		if err == nil {
			return 0, out.String(), nil
		}
		var ee *ssh.ExitError
		if errors.As(err, &ee) {
			return ee.ExitStatus(), out.String(), nil
		}
		return 0, out.String(), fmt.Errorf("deviceops: ssh run: %w", err)
	case <-sctx.Done():
		_ = sess.Close()
		<-errc //nolint:errcheck // waits out the run so out is whole; its error is the close's
		return 0, out.String(), errTimeout
	}
}

func (t *sshTransport) writeFile(ctx context.Context, acc access, path string, mode uint32, data []byte) error {
	if badPath(path) {
		return fmt.Errorf("deviceops: refusing a path sftp cannot name: %q", path)
	}
	c, _, err := t.dial(ctx, acc)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	sctx, cancel := context.WithTimeout(ctx, longShell)
	defer cancel()
	ch, err := c.NewSession()
	if err != nil {
		return fmt.Errorf("deviceops: ssh session: %w", err)
	}
	// sftp over the session channel (no extra dial)
	ioChan := make(chan error, 1)
	var client *sftp.Client
	go func() {
		w, err := ch.StdinPipe()
		if err != nil {
			ioChan <- err
			return
		}
		r, err := ch.StdoutPipe()
		if err != nil {
			ioChan <- err
			return
		}
		if err := ch.RequestSubsystem("sftp"); err != nil {
			ioChan <- err
			return
		}
		client, err = sftp.NewClientPipe(r, w)
		ioChan <- err
	}()
	select {
	case err := <-ioChan:
		if err != nil {
			_ = ch.Close()
			return fmt.Errorf("deviceops: sftp: %w", err)
		}
	case <-sctx.Done():
		_ = ch.Close()
		return errTimeout
	}
	defer func() { _ = client.Close() }()
	tmp := path + ".cadence-new"
	f, err := client.Create(tmp)
	if err != nil {
		return fmt.Errorf("deviceops: sftp create: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("deviceops: sftp write: %w", err)
	}
	if err := f.Chmod(os.FileMode(fsMode(mode))); err != nil {
		_ = f.Close()
		return fmt.Errorf("deviceops: sftp mode: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("deviceops: sftp close: %w", err)
	}
	if err := client.Rename(tmp, path); err != nil {
		// posix-rename when the server has it, else remove+rename
		if err2 := client.Remove(path); err2 == nil {
			if err3 := client.Rename(tmp, path); err3 == nil {
				return nil
			}
		}
		return fmt.Errorf("deviceops: sftp rename: %w", err)
	}
	return nil
}

func (t *sshTransport) readHash(ctx context.Context, acc access, path string) (string, error) {
	if badPath(path) {
		return "", fmt.Errorf("deviceops: refusing a path the shell cannot name: %q", path)
	}
	_, out, err := t.exec(ctx, acc, "sha256sum '"+path+"'", 2*time.Minute)
	if err != nil {
		return "", err
	}
	f := strings.Fields(out)
	if len(f) == 0 || len(f[0]) != 64 {
		return "", fmt.Errorf("deviceops: no hash for %s", path)
	}
	return f[0], nil
}

func fsMode(m uint32) (f uint32) {
	if m == 0 {
		return 0o644
	}
	return m
}
