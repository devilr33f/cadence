package telemetry_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"google.golang.org/protobuf/proto"

	"git.shino.boo/devilreef/cadence/server/internal/telemetry"
)

// a bomb of n MiB of zeros
func bomb(n int) []byte {
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	zeros := make([]byte, 1<<20)
	for range n {
		_, _ = w.Write(zeros)
	}
	_ = w.Close()
	return b.Bytes()
}

// postGzipped sends the gzipped body from token, from any goroutine; a failed send is status 0
func (r *rig) postGzipped(path, token string, body []byte) answer {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	for k, v := range gzipped(token) {
		req.Header.Set(k, v)
	}
	resp, err := r.app.Test(req, fiber.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		return answer{}
	}
	_ = resp.Body.Close()
	return answer{status: resp.StatusCode, header: resp.Header}
}

// postAll sends the gzipped logs body once from each token, all at once
func (r *rig) postAll(tokens []string, body []byte) []answer {
	out := make([]answer, len(tokens))
	var wg sync.WaitGroup
	for i, token := range tokens {
		wg.Go(func() { out[i] = r.postGzipped(logsPath, token, body) })
	}
	wg.Wait()
	return out
}

func repeat(token string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = token
	}
	return out
}

// statuses counts the answers by status
func statuses(as []answer) map[int]int {
	out := map[int]int{}
	for _, a := range as {
		out[a.status]++
	}
	return out
}

// bombs from many devices at once: at most 4 bodies are decompressed at once,
// server-wide, and each device pays for its bomb's 16 MiB
func TestConcurrentBombsBounded(t *testing.T) {
	r := newRig(t)
	tokens := []string{tokenA, tokenB}
	for i := range 6 {
		tokens = append(tokens, r.enroll(i))
	}
	var (
		running atomic.Int32
		mu      sync.Mutex
		most    int32
	)
	telemetry.SetGunzip(r.in, func(b []byte, limit int) ([]byte, error) {
		n := running.Add(1)
		defer running.Add(-1)
		mu.Lock()
		most = max(most, n)
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		return telemetry.Gunzip(b, limit)
	}, 5*time.Second)
	if s := statuses(r.postAll(tokens, bomb(32))); s[413] != len(tokens) {
		t.Fatalf("bombs: %v", s)
	}
	mu.Lock()
	m := most
	mu.Unlock()
	if m > 4 || m < 2 {
		t.Fatalf("%d decompressed at once", m)
	}
	// 16 MiB charged: the device waits, one that sent no bomb does not
	if a := r.post(logsPath, sized(1<<10), gzipped(tokenA)); a.status != 429 {
		t.Fatalf("after the bombs: %d", a.status)
	}
	if a := r.post(logsPath, sized(1<<10), gzipped(r.enroll(99))); a.status != 200 {
		t.Fatalf("other device: %d", a.status)
	}
	// slots held too long: the rest are told to come back
	telemetry.SetGunzip(r.in, func(b []byte, limit int) ([]byte, error) {
		time.Sleep(200 * time.Millisecond)
		return telemetry.Gunzip(b, limit)
	}, 10*time.Millisecond)
	fresh := make([]string, 8)
	for i := range fresh {
		fresh[i] = r.enroll(10 + i)
	}
	s := statuses(r.postAll(fresh, sized(1<<10)))
	if s[200] < 4 || s[503] < 1 || s[200]+s[503] != 8 {
		t.Fatalf("statuses %v", s)
	}
	// each refusal is counted by its kind
	logs, metrics := r.in.Stats()
	if logs.Busy != uint64(s[503]) || logs.Failed["too_large"] != uint64(len(tokens)) || logs.RateLimited != 1 ||
		logs.Accepted != uint64(1+s[200]) || metrics.Accepted+metrics.Rejected+metrics.RateLimited+metrics.Busy != 0 {
		t.Fatalf("logs %+v metrics %+v", logs, metrics)
	}
}

func countDeviceLogs(t *testing.T, r *rig, device string) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM device_logs WHERE device_id = $1::uuid`, device).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// many small requests at once that each decompress to 10 MiB of valid lines:
// one of a device's per signal is in flight at a time, so only one gets past its
// bucket, and the others' slots stay free for other devices
func TestOneRequestInFlightPerDeviceAndSignal(t *testing.T) {
	r := newRig(t)
	telemetry.SetGunzip(r.in, func(b []byte, limit int) ([]byte, error) {
		time.Sleep(300 * time.Millisecond)
		return telemetry.Gunzip(b, limit)
	}, 100*time.Millisecond)
	var others []answer
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(50 * time.Millisecond)
		for range 3 {
			others = append(others, r.postAll([]string{tokenB}, sized(1<<10))...)
		}
	}()
	got := r.postAll(repeat(tokenA, 32), sized(10<<20))
	<-done
	if s := statuses(got); s[200] != 1 || s[429] != 31 {
		t.Fatalf("one device's requests: %v", s)
	}
	for _, a := range got {
		if a.status == 429 && a.header.Get("Retry-After") == "" {
			t.Fatal("a 429 without Retry-After")
		}
	}
	if n := countDeviceLogs(t, r, r.devA); n != 1 {
		t.Fatalf("stored %d", n)
	}
	if s := statuses(others); s[200] != 3 {
		t.Fatalf("the other device: %v", s)
	}
}

// a device's logs and metrics in flight together: the one that finds the
// bucket in debt once its size is known, the other having left it there, is refused
func TestRefusedWhenTheOtherSignalLeftDebt(t *testing.T) {
	r := newRig(t)
	logs := sized(10 << 20)
	telemetry.SetGunzip(r.in, func(b []byte, limit int) ([]byte, error) {
		// the logs finish first
		if len(b) == len(logs) {
			time.Sleep(50 * time.Millisecond)
		} else {
			time.Sleep(300 * time.Millisecond)
		}
		return telemetry.Gunzip(b, limit)
	}, 5*time.Second)
	m, _ := proto.Marshal(metricsRequest(gauge("g", dbl(start, 1))))
	var la, ma answer
	var wg sync.WaitGroup
	wg.Go(func() { la = r.postGzipped(logsPath, tokenA, logs) })
	wg.Go(func() { ma = r.postGzipped(metricsPath, tokenA, gz(m)) })
	wg.Wait()
	if la.status != 200 || ma.status != 429 {
		t.Fatalf("logs %d, metrics %d", la.status, ma.status)
	}
	if s, _ := strconv.Atoi(ma.header.Get("Retry-After")); s < 2 || s > 3 {
		t.Fatalf("retry-after %q", ma.header.Get("Retry-After"))
	}
	if n := len(r.storedPoints()); n != 0 {
		t.Fatalf("stored %d points", n)
	}
}
