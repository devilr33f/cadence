package devices

import "time"

func SetDBTimeout(s *Service, d time.Duration) { s.dbTimeout = d }

func SetNow(s *Service, now func() time.Time) { s.now = now }

func SecretAAD(deviceID, capability, name string) []byte {
	return secretAAD(deviceID, capability, name)
}
