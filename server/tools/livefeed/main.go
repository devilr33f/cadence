// Command livefeed waits on the live feed for one event, for the e2e: it dials
// -url with the bearer token in CADENCE_ADMIN_TOKEN, or with the cookie
// header value in CADENCE_FEED_COOKIE, prints "connected" once the feed
// confirmed its connection, so that every event published after it arrives,
// and exits 0 at the first event of -type whose data.count is at least
// -min-count and, with -source, whose data.source is -source. It prints that
// event's type and count only, never its data: the feed carries transcripts
// and log lines.
//
// -origin sets the Origin header. -subscribe sends a subscription of those
// topics after the first frame and waits for its answer. -expect-status
// passes when the upgrade is refused with that status, and -expect-error when
// the subscription is refused with that code. -only fails the run when an
// event of any other type arrives, and -linger keeps listening that long
// after the event; a feed that closes meanwhile fails it too.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// options is one run of the helper.
type options struct {
	url, token, cookie, origin string
	typ, source, trigger       string
	minCount                   int
	subscribe                  []string
	subscribeSet               bool
	expectStatus               int
	expectError                string
	only                       []string
	linger                     time.Duration
}

func main() {
	var o options
	flag.StringVar(&o.url, "url", "", "the feed, ws://host:port/api/v1/live?deviceId=...")
	flag.StringVar(&o.typ, "type", "", "the event type to wait for")
	flag.IntVar(&o.minCount, "min-count", 0, "the least data.count of the event")
	flag.StringVar(&o.source, "source", "", "the data.source of the event, any when empty")
	flag.StringVar(&o.trigger, "trigger", "", "the data.trigger of the event, any when empty")
	flag.StringVar(&o.origin, "origin", "", "the Origin header to send")
	sub := flag.String("subscribe", "", "topics to subscribe to, comma separated (empty value: none)")
	flag.IntVar(&o.expectStatus, "expect-status", 0, "pass when the upgrade is refused with this status")
	flag.StringVar(&o.expectError, "expect-error", "", "pass when the subscription is refused with this error code")
	only := flag.String("only", "", "the only event types that may arrive, comma separated (empty: any)")
	flag.DurationVar(&o.linger, "linger", 0, "keep listening this long after the event")
	timeout := flag.Duration("timeout", 30*time.Second, "how long to wait")
	flag.Parse()
	flag.Visit(func(f *flag.Flag) { o.subscribeSet = o.subscribeSet || f.Name == "subscribe" })
	o.subscribe = split(*sub)
	o.only = split(*only)
	o.token = os.Getenv("CADENCE_ADMIN_TOKEN")
	o.cookie = os.Getenv("CADENCE_FEED_COOKIE")
	if o.url == "" || (o.token == "" && o.cookie == "") || (o.typ == "" && o.expectStatus == 0 && o.expectError == "") {
		fmt.Fprintln(os.Stderr, "livefeed: -url, -type (or an -expect-*) and CADENCE_ADMIN_TOKEN or CADENCE_FEED_COOKIE are required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	err := runOpts(ctx, o, os.Stdout)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "livefeed:", err)
		os.Exit(1)
	}
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func run(ctx context.Context, url, token, typ, source string, minCount int, out io.Writer) error {
	return runOpts(ctx, options{url: url, token: token, typ: typ, source: source, minCount: minCount}, out)
}

func runOpts(ctx context.Context, o options, out io.Writer) error {
	h := http.Header{}
	if o.cookie != "" {
		h.Set("Cookie", o.cookie)
	} else {
		h.Set("Authorization", "Bearer "+o.token)
	}
	if o.origin != "" {
		h.Set("Origin", o.origin)
	}
	c, resp, err := websocket.Dial(ctx, o.url, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		if o.expectStatus != 0 && resp != nil && resp.StatusCode == o.expectStatus {
			_, _ = fmt.Fprintf(out, "refused status=%d\n", resp.StatusCode)
			return nil
		}
		if resp != nil {
			return fmt.Errorf("dial answered %d", resp.StatusCode)
		}
		return errors.New("dial failed")
	}
	defer func() { _ = c.CloseNow() }()
	if o.expectStatus != 0 {
		return fmt.Errorf("the upgrade was not refused with %d", o.expectStatus)
	}
	// the feed's first frame confirms its connection: what is published after it arrives
	if f, err := next(ctx, c); err != nil || f.Type != "subscribed" {
		return errors.New("the feed did not confirm its subscription")
	}
	if o.subscribeSet {
		b, _ := json.Marshal(map[string][]string{"subscribe": append([]string{}, o.subscribe...)})
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			return errors.New("subscription not sent")
		}
		f, err := next(ctx, c)
		if err != nil {
			return errors.New("the feed did not answer the subscription")
		}
		if f.Error != "" {
			if f.Error == o.expectError {
				_, _ = fmt.Fprintf(out, "refused error=%s\n", f.Error)
				return nil
			}
			return fmt.Errorf("the subscription was refused with %q", f.Error)
		}
		if f.Type != "subscribed" {
			return errors.New("the feed did not confirm the subscription")
		}
	}
	if o.expectError != "" {
		return fmt.Errorf("the subscription was not refused with %q", o.expectError)
	}
	_, _ = fmt.Fprintln(out, "connected")
	var until time.Time
	for {
		rd, cancel := ctx, func() {}
		if !until.IsZero() {
			rd, cancel = context.WithDeadline(ctx, until)
		}
		f, err := next(rd, c)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("no %s with count >= %d in time", o.typ, o.minCount)
			}
			if !until.IsZero() && errors.Is(err, context.DeadlineExceeded) {
				_, _ = fmt.Fprintln(out, "quiet")
				return nil
			}
			return errors.New("feed closed")
		}
		if len(o.only) > 0 && !slices.Contains(o.only, f.Type) {
			return fmt.Errorf("an event of the type %s arrived", f.Type)
		}
		if !until.IsZero() || f.Type != o.typ || f.Data.Count < o.minCount || (o.source != "" && f.Data.Source != o.source) ||
			(o.trigger != "" && f.Data.Trigger != o.trigger) {
			continue
		}
		_, _ = fmt.Fprintf(out, "%s count=%d\n", o.typ, f.Data.Count)
		if o.linger == 0 {
			_ = c.Close(websocket.StatusNormalClosure, "")
			return nil
		}
		until = time.Now().Add(o.linger)
	}
}

// frame is what the helper reads of a feed frame; its other fields are never kept.
type frame struct {
	Type  string `json:"type"`
	Error string `json:"error"`
	Data  struct {
		Count   int    `json:"count"`
		Source  string `json:"source"`
		Trigger string `json:"trigger"`
	} `json:"data"`
}

// next reads the next frame; one that is no JSON object reads as a frame of no type.
func next(ctx context.Context, c *websocket.Conn) (frame, error) {
	var f frame
	_, b, err := c.Read(ctx)
	if err != nil {
		return f, err
	}
	_ = json.Unmarshal(b, &f)
	return f, nil
}
