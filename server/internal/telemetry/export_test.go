package telemetry

import "time"

// SetFlushEvery sets how long a batch of the server's lines waits; set it before Run.
func SetFlushEvery(s *SelfLog, d time.Duration) { s.every = d }

var KeepBody = keepBody

// PointAttrs is the JSON a NumberDataPoint's attributes are stored as.
func PointAttrs(b []byte) (string, bool) {
	p, ok, _ := parsePoint(b)
	return string(p.attrs), ok
}

var Gunzip = gunzip

// SetGunzip replaces in's decompression and how long a request waits for a slot; set it before requests.
func SetGunzip(in *Ingest, fn func(b []byte, limit int) ([]byte, error), wait time.Duration) {
	in.gunzip, in.slotWait = fn, wait
}

// SetStoreTimeout sets how long a store call of the server's lines may take; set it before Run.
func SetStoreTimeout(s *SelfLog, d time.Duration) { s.timeout = d }

// SetExportBackoff sets the first wait after a failure and the longest; set it before Run.
func SetExportBackoff(e *Exporter, first, most time.Duration) {
	e.backoffMin, e.backoffMax = first, most
}

// SetAuthTimeout sets how long a request's token lookup may take; set it before requests.
func SetAuthTimeout(in *Ingest, d time.Duration) { in.authTimeout = d }

// SetAuthWarnEvery sets how long a token lookup's failure warning holds back the next ones; set it before requests.
func SetAuthWarnEvery(in *Ingest, d time.Duration) { in.authWarn.every = d }
