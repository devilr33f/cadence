package install

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// fileMode is the one form of mode WriteFile takes: chmod's octal
var fileMode = regexp.MustCompile(`^0?[0-7]{3}$`)

// WriteFile pushes data to p's staging name and renames it over p only once its sha256 on the device is data's; a
// staged copy that arrived different is removed (status 6), p untouched. Only the staged copy is ever removed
func (s *Shell) WriteFile(ctx context.Context, p string, data []byte, mode string) error {
	if !fileMode.MatchString(mode) {
		return fmt.Errorf("writing %s: mode %q is not octal", p, mode)
	}
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would write %s (%d bytes, sha256 %s, mode %s)\n", p, len(data), sum(data), mode)
		return nil
	}
	tmp, err := s.pushStaged(ctx, p, data)
	if err != nil {
		return err
	}
	res, err := s.run(ctx, fmt.Sprintf(`h=$(sha256sum < %s) && [ "${h%%%% *}" = %s ] || { rm -f %s; exit 6; }; chmod %s %s && mv -f %s %s`,
		s.at(tmp), quote(sum(data)), s.at(tmp), mode, s.at(tmp), s.at(tmp), s.at(p)))
	switch {
	case err != nil:
		return err
	case res.Code == 6:
		return fmt.Errorf("%w: %s arrived different on the device: not written", ErrRefused, p)
	case res.Code != 0:
		return fmt.Errorf("writing %s failed (status %d)%s", p, res.Code, tail(res.Out))
	}
	return nil
}

// CopyFile copies src to dst's staging name on the device and renames it over dst only once the copy's sha256 is
// src's; a copy that differs is removed (status 6), dst untouched. Only the staged copy is ever removed
func (s *Shell) CopyFile(ctx context.Context, src, dst, mode string) error {
	if !fileMode.MatchString(mode) {
		return fmt.Errorf("copying %s: mode %q is not octal", src, mode)
	}
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would copy %s to %s (mode %s)\n", src, dst, mode)
		return nil
	}
	tmp := s.at(staging(dst))
	res, err := s.run(ctx, fmt.Sprintf(`cp %s %s && h=$(sha256sum < %s) && g=$(sha256sum < %s) && [ "${h%%%% *}" = "${g%%%% *}" ] || { rm -f %s; exit 6; }; chmod %s %s && mv -f %s %s`,
		s.at(src), tmp, tmp, s.at(src), tmp, mode, tmp, tmp, s.at(dst)))
	switch {
	case err != nil:
		return err
	case res.Code == 6:
		return fmt.Errorf("%w: %s copied different on the device: not written", ErrRefused, dst)
	case res.Code != 0:
		return fmt.Errorf("copying %s to %s failed (status %d)%s", src, dst, res.Code, tail(res.Out))
	}
	return nil
}

// sh runs a boot script as init would: with the board's boot PATH, not the session's, when it names one
func (s *Shell) sh() string {
	if s.opts.BootPath == "" {
		return "sh"
	}
	return "PATH=" + quote(s.opts.BootPath) + " sh"
}

// scriptArg is the one form of argument a boot script gets (start, stop, release), which also names its output file
var scriptArg = regexp.MustCompile(`^[a-z]+$`)

// captureDir is where boot scripts' output goes: the stage dir where the board names one (a tmpfs on an overlay
// board), else the work dir
func (s *Shell) captureDir() string {
	if s.opts.StageDir != "" {
		return s.opts.StageDir
	}
	return s.opts.Work
}

// captured is the file in the capture dir a boot script's output goes to, quoted. the daemons it leaves running keep
// that file as their stdout and stderr, so it is never the session's pipe: once the session ends the pipe has no
// reader, and a daemon's next write there would be its SIGPIPE. each script and argument has a file of its own, so one
// daemon's late lines never land in another script's output. the file is removed once read, and what a daemon writes
// to it after costs the capture dir's space until the daemon exits: a little RAM where it is a tmpfs
func (s *Shell) captured(script, arg string) (string, error) {
	if s.captureDir() == "" {
		return "", errors.New("running " + script + ": no work dir for its output")
	}
	if !scriptArg.MatchString(arg) {
		return "", fmt.Errorf("running %s: argument %q", script, arg)
	}
	return s.at(path.Join(s.captureDir(), path.Base(script)+"."+arg+".out")), nil
}

// captureReady is a command that makes the capture dir, mode 700, unless it is there
func (s *Shell) captureReady() string {
	d := s.at(s.captureDir())
	return fmt.Sprintf("{ [ -d %s ] || { mkdir -p %s && chmod 700 %s; }; }", d, d, d)
}

// boot is a command that runs script with arg as init would, its input /dev/null and its output into file
func (s *Shell) boot(script, arg, file string) string {
	return fmt.Sprintf("%s %s %s </dev/null >%s 2>&1", s.sh(), s.at(script), quote(arg), file)
}

// RunScript runs an init script with one argument, as rcS does, and reads what it printed from its file once it is
// done; a status other than 0 is an error carrying the script's last line
func (s *Shell) RunScript(ctx context.Context, script, arg string) (string, error) {
	file, err := s.captured(script, arg)
	if err != nil {
		return "", err
	}
	cmd := fmt.Sprintf("%s || exit; %s; rc=$?; cat %s; rm -f %s; exit $rc", s.captureReady(), s.boot(script, arg, file), file, file)
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would run: %s\n", cmd)
		return "", nil
	}
	res, err := s.run(ctx, cmd)
	switch {
	case err != nil:
		return "", err
	case res.Code != 0:
		return string(res.Out), fmt.Errorf("%s %s failed (status %d)%s", script, arg, res.Code, tail(res.Out))
	}
	return string(res.Out), nil
}

// handOverMark parts the release's output from the start's, and handOverStatus carries both statuses at the end
const (
	handOverMark   = "cadence-handover-start"
	handOverStatus = "cadence-handover="
)

// HandOver runs the wrapper's release and then script's start in one command, so no new session sits between them,
// and nothing is written to the session until both are done: a session cut after the release cannot stop the start
// with a SIGPIPE. the start runs whatever the release did, and returns its output. A failed release carries the
// hook's own line (event tokens only)
func (s *Shell) HandOver(ctx context.Context, wrapper, script string) (string, error) {
	rel, err := s.captured(wrapper, "release")
	if err != nil {
		return "", err
	}
	start, err := s.captured(script, "start")
	if err != nil {
		return "", err
	}
	cmd := fmt.Sprintf(`%s || exit; %s; r=$?; %s; t=$?; cat %s; echo; echo %s; cat %s; rm -f %s %s; echo; echo %s"$r $t"`,
		s.captureReady(), s.boot(wrapper, "release", rel), s.boot(script, "start", start), rel, handOverMark, start, rel,
		start, handOverStatus)
	if s.opts.DryRun {
		_, _ = fmt.Fprintf(s.opts.Out, "cadence-install: would run: %s\n", cmd)
		return "", nil
	}
	res, err := s.run(ctx, cmd)
	if err != nil {
		return "", err
	}
	out := string(res.Out)
	var released, started int
	i := strings.LastIndex(out, "\n"+handOverStatus)
	if i >= 0 {
		_, err = fmt.Sscanf(out[i+1+len(handOverStatus):], "%d %d", &released, &started)
	}
	if res.Code != 0 || i < 0 || err != nil {
		return "", fmt.Errorf("handing over to %s failed (status %d)", script, res.Code)
	}
	relOut, startOut, _ := strings.Cut(out[:i], "\n"+handOverMark+"\n")
	var errs []error
	if released != 0 {
		errs = append(errs, fmt.Errorf("the board's release failed (status %d)%s", released, tail([]byte(relOut))))
	}
	if started != 0 {
		errs = append(errs, fmt.Errorf("%s start failed (status %d)%s", script, started, tail([]byte(startOut))))
	}
	return startOut, errors.Join(errs...)
}
