package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// bound is ctx with timeout d that also ends when the session is closed, and
// the flag its requests' bodies set past the cap.
func bound(ctx context.Context, s *session, d time.Duration) (context.Context, *atomic.Bool, context.CancelFunc) {
	cctx, cancel := context.WithTimeout(ctx, d)
	stop := context.AfterFunc(s.ctx, cancel)
	cctx, over := withOverflow(cctx)
	return cctx, over, func() {
		stop()
		cancel()
	}
}

// failure turns err from a request on s under cctx (bound from ctx) into the
// caller's error: the caller's own cancellation as itself, a deadline as
// mcp_timeout, a session closed under it as mcp_unavailable, its own body
// over the cap as mcp_protocol, else a code by type.
func failure(ctx, cctx context.Context, s *session, over *atomic.Bool, err error, o op) error {
	switch {
	case ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ctx.Err()
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		return fail(CodeTimeout)
	case s.ctx.Err() != nil:
		return fail(CodeUnavailable)
	case over.Load():
		return &Error{Code: CodeProtocol, brk: true}
	}
	return classify(err, o)
}

// ListTools lists every page on the current session within 10 s. A
// repeated cursor, more than 20 pages or more than 512 tools fail it with
// mcp_protocol, never a partial listing, and leave the session in service.
// In the modern era the SDK may answer from its cache for the server's
// ttlMs; Refresh never does.
func (c *Client) ListTools(ctx context.Context) ([]RemoteTool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := c.acquire()
	if err != nil {
		return nil, err
	}
	defer s.inflight.Done()
	tools, err := c.list(ctx, s)
	c.breakOn(s, err)
	return tools, err
}

// Refresh lists the server's tools as they are now: in the modern era on a
// session of its own, so that no cached listing answers. A failure that says
// the server cannot serve breaks the current session as a listing would;
// closing the client aborts it with mcp_unavailable.
func (c *Client) Refresh(ctx context.Context) ([]RemoteTool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := c.acquire()
	if err != nil {
		return nil, err
	}
	defer s.inflight.Done()
	// the dial and the listing end with the current session too, so Close
	// never waits on them
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.ctx, cancel)()
	tools, err := c.refresh(rctx, s)
	if err != nil && ctx.Err() == nil && s.ctx.Err() != nil {
		return nil, fail(CodeUnavailable)
	}
	c.breakOn(s, err)
	return tools, err
}

func (c *Client) refresh(ctx context.Context, s *session) ([]RemoteTool, error) {
	if s.era != EraModern {
		return c.list(ctx, s)
	}
	on, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer c.retire(on)
	defer on.inflight.Done()
	return c.list(ctx, on)
}

func (c *Client) list(ctx context.Context, s *session) ([]RemoteTool, error) {
	lctx, over, cancel := bound(ctx, s, c.t.list)
	defer cancel()
	var out []RemoteTool
	seen := map[string]bool{}
	cursor := ""
	for range maxPages {
		res, err := s.cs.ListTools(lctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, failure(ctx, lctx, s, over, err, opList)
		}
		if len(out)+len(res.Tools) > maxTools {
			return nil, fail(CodeProtocol)
		}
		for _, t := range res.Tools {
			schema, err := json.Marshal(t.InputSchema)
			if err != nil {
				return nil, fail(CodeProtocol)
			}
			out = append(out, RemoteTool{Name: t.Name, Description: t.Description, InputSchema: schema, Annotations: t.Annotations})
		}
		if res.NextCursor == "" {
			return out, nil
		}
		if seen[res.NextCursor] {
			return nil, fail(CodeProtocol)
		}
		seen[res.NextCursor] = true
		cursor = res.NextCursor
	}
	return nil, fail(CodeProtocol)
}

// CallTool runs one tools/call within the configured timeout or ctx's
// earlier deadline, and never retries it: a failed call may have had its
// effect. isError is mcp_tool_error with none of the server's text. A call
// that fails leaves the others in flight alone. One whose deadline passed is
// mcp_timeout, never sent; a cancelled one comes back as ctx's error.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*Result, error) {
	if err := ctx.Err(); errors.Is(err, context.DeadlineExceeded) {
		return nil, &Error{Code: CodeTimeout, unsent: true}
	} else if err != nil {
		return nil, err
	}
	s, err := c.acquire()
	if err != nil {
		return nil, err
	}
	defer s.inflight.Done()
	if args == nil {
		args = map[string]any{}
	}
	cctx, over, cancel := bound(ctx, s, c.timeout)
	defer cancel()
	res, err := s.cs.CallTool(cctx, &sdk.CallToolParams{Name: name, Arguments: args})
	switch {
	case err != nil:
		err = failure(ctx, cctx, s, over, err, opCall)
		c.breakOn(s, err)
		return nil, err
	case res.IsError:
		return nil, fail(CodeToolError)
	case res.NeedsInput():
		return nil, fail(CodeProtocol)
	}
	return &Result{Structured: res.StructuredContent, Content: res.Content}, nil
}
