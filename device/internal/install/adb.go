package install

import (
	"context"
)

// ADB runs commands over network adb, the fallback when ssh does not answer. Every call names the device with -s:
// another one may be connected.
type ADB struct {
	// Serial is ip:port
	Serial string
	// Bin is the adb program, "adb" when empty
	Bin  string
	exec execFunc
}

func (a *ADB) String() string { return "adb " + a.Serial }

// Connect asks the adb server to connect to the device; a failure shows in the first command
func (a *ADB) Connect(ctx context.Context) {
	_, _, _ = a.runner()(ctx, a.bin(), []string{"connect", a.Serial}, nil, nil)
}

func (a *ADB) Run(ctx context.Context, script string) (Result, error) {
	out, errOut, err := a.runner()(ctx, a.bin(), []string{"-s", a.Serial, "shell", wrap(script)}, nil, nil)
	if res, ok := unwrap(out); ok {
		return res, nil
	}
	return Result{}, transportError(a.String(), errOut, err)
}

// Push uses adb push, whose own status is real
func (a *ADB) Push(ctx context.Context, local, path string) error {
	_, errOut, err := a.runner()(ctx, a.bin(), []string{"-s", a.Serial, "push", local, path}, nil, nil)
	if err != nil {
		return transportError(a.String(), errOut, err)
	}
	return nil
}

func (a *ADB) bin() string {
	if a.Bin == "" {
		return "adb"
	}
	return a.Bin
}

func (a *ADB) runner() execFunc {
	if a.exec == nil {
		return hostExec
	}
	return a.exec
}
