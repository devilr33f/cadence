package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/discovery"
)

// browse is discovery.Browse; tests answer it themselves
var browse = discovery.Browse

// runDiscover lists the cadence devices that answer on the local network: enrolled devices advertise themselves
// (network.discovery), so an unenrolled one or one switched off is not listed
func runDiscover(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cadence-install discover", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	wait := fs.Duration("wait", 3*time.Second, "how long to listen for answers")
	asJSON := fs.Bool("json", false, "print the devices as JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *wait <= 0 || *wait > time.Minute {
		msg := "--wait takes a duration up to 1m"
		if err != nil {
			msg = err.Error()
		} else if fs.NArg() > 0 {
			msg = "unexpected argument " + fs.Arg(0)
		}
		_, _ = fmt.Fprintf(stderr, "cadence-install: %s\n%s\n", msg, usage)
		return exitUsage
	}
	devices, err := browse(ctx, *wait)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cadence-install: discover: %v\n", err)
		return exitFailed
	}
	if *asJSON {
		type out struct {
			Instance string   `json:"instance"`
			DeviceID string   `json:"deviceId"`
			Board    string   `json:"board"`
			Version  string   `json:"version"`
			Host     string   `json:"host"`
			Addrs    []string `json:"addresses"`
		}
		list := make([]out, 0, len(devices))
		for _, d := range devices {
			o := out{Instance: d.Instance, DeviceID: d.DeviceID, Board: d.Board, Version: d.Version, Host: d.Host, Addrs: []string{}}
			for _, a := range d.Addrs {
				o.Addrs = append(o.Addrs, a.String())
			}
			list = append(list, o)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(list); err != nil {
			return exitFailed
		}
		return 0
	}
	if len(devices) == 0 {
		_, _ = fmt.Fprintln(stdout, "cadence-install: no cadence device answered")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "BOARD\tDEVICE ID\tVERSION\tHOST\tADDRESSES")
	for _, d := range devices {
		addrs := make([]string, 0, len(d.Addrs))
		for _, a := range d.Addrs {
			addrs = append(addrs, a.String())
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.Board, d.DeviceID, d.Version, d.Host, strings.Join(addrs, ","))
	}
	if err := tw.Flush(); err != nil {
		return exitFailed
	}
	return 0
}
