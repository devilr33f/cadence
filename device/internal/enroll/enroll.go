// Package enroll trades a one-time code for a device id and token and saves them in the config.
package enroll

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/device/internal/config"
	"git.shino.boo/devilreef/cadence/device/internal/syncstore"
)

var (
	ErrRefused = errors.New("enroll refused")
	ErrInvalid = errors.New("invalid enroll options")
)

type Options struct {
	Server, Code, Board, ConfigPath, DataDir, CAFile string
	Audiod                                           bool
	AudiodBinary                                     string
	Client                                           *http.Client
}

const maxResponseBytes = 64 << 10

// a seam so tests can fail staging without file permissions
var stageConfig = config.Stage

func Run(ctx context.Context, o Options) error {
	server := strings.TrimRight(o.Server, "/")
	if server == "" || o.Code == "" || o.ConfigPath == "" {
		return fmt.Errorf("%w: server, code and config are required", ErrInvalid)
	}
	next := config.Config{Board: o.Board, ServerURL: server, DeviceID: "pending", DeviceToken: "pending", DataDir: o.DataDir, CAFile: o.CAFile, Audiod: o.Audiod, AudiodBinary: o.AudiodBinary}
	if err := config.Validate(next); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	client, err := httpClient(o)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"code": o.Code, "fingerprint": map[string]string{"board": o.Board}})
	if err != nil {
		return errors.New("enroll: encode request failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/v2/enroll", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: server url", ErrInvalid)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// the transport error may quote the url; keep only that the request failed
		return errors.New("enroll: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return ErrRefused
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("enroll: unexpected status %d", resp.StatusCode)
	}
	var out struct {
		DeviceID string `json:"deviceId"`
		Token    string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil || out.DeviceID == "" || out.Token == "" {
		return errors.New("enroll: malformed response")
	}
	return save(o, next, out.DeviceID, out.Token)
}

// Save writes the config of a device the server enrolled with id and token another way (a screen's claim): as Run
// does once its code is taken.
func Save(o Options, deviceID, token string) error {
	server := strings.TrimRight(o.Server, "/")
	next := config.Config{Board: o.Board, ServerURL: server, DeviceID: "pending", DeviceToken: "pending", DataDir: o.DataDir, CAFile: o.CAFile, Audiod: o.Audiod, AudiodBinary: o.AudiodBinary}
	if err := config.Validate(next); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if deviceID == "" || token == "" {
		return errors.New("enroll: malformed response")
	}
	return save(o, next, deviceID, token)
}

func save(o Options, next config.Config, deviceID, token string) error {
	next.DeviceID, next.DeviceToken = deviceID, token
	// the stored configs belong to the old enrollment, and the server counts
	// generations afresh; cleared only once the new config is staged, and
	// before it appears, which a waiting daemon starts on
	commit, abort, err := stageConfig(o.ConfigPath, next)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(syncstore.Dir(o.DataDir)); err != nil {
		abort()
		return errors.New("enroll: cannot clear the config store")
	}
	if err := commit(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Check refuses options Save would refuse (ErrInvalid), before anything is asked of the server.
func Check(o Options) error {
	next := config.Config{Board: o.Board, ServerURL: strings.TrimRight(o.Server, "/"), DeviceID: "pending", DeviceToken: "pending", DataDir: o.DataDir, CAFile: o.CAFile, Audiod: o.Audiod, AudiodBinary: o.AudiodBinary}
	if err := config.Validate(next); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

// HTTPClient is the client Run uses for the server: its CA file trusted, no redirects.
func HTTPClient(o Options) (*http.Client, error) { return httpClient(o) }

func httpClient(o Options) (*http.Client, error) {
	c := &http.Client{Timeout: 30 * time.Second}
	if o.Client != nil {
		cp := *o.Client
		c = &cp
	} else {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if o.CAFile != "" {
			pem, err := os.ReadFile(o.CAFile)
			if err != nil {
				return nil, errors.New("enroll: cannot read ca file")
			}
			if !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("enroll: ca file has no certificates")
			}
		}
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }
	return c, nil
}
