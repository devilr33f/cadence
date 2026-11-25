// Package apitest sends requests a test client cannot build, such as a target
// with a malformed percent-escape.
package apitest

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// Get serves app on a loopback listener and sends GET target, as written, with
// the admin bearer; it returns the status and body.
func Get(t *testing.T, app *fiber.App, target, bearer string) (int, string) {
	t.Helper()
	return Do(t, app, "GET", target, bearer)
}

// Do is Get for another method (no body).
func Do(t *testing.T, app *fiber.App, method, target, bearer string) (int, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	req := method + " " + target + " HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + bearer +
		"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}
