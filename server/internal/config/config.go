// Package config reads the bootstrap environment.
package config

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type YandexProxyConfig struct {
	Listen     string
	CACertFile string
	CAKeyFile  string
	Clients    []netip.Prefix
	// Devices maps the vendor's DeviceId in a telemetry batch to a Cadence device.
	Devices map[string]string
	// UpstreamDNS is an optional host:port resolver used only for upstream connections.
	UpstreamDNS string
}

type Config struct {
	Listen        string
	DatabaseURL   string
	MasterKeyFile string
	YandexProxy   YandexProxyConfig
	DevFakes      bool
	// DevCompactUnit is one compactAfter step, zero for a minute: the dev
	// stack shortens it (CADENCE_DEV_COMPACT_SECONDS) so that the e2e sees
	// a compaction within seconds. It is read only beside the fakes.
	DevCompactUnit time.Duration
	// DevAwake is how long a session the user began keeps quiet hours from
	// holding task updates, zero for the 2 min default: the dev stack
	// shortens it (CADENCE_DEV_AWAKE_SECONDS) so that the e2e waits seconds.
	// It is read only beside the fakes.
	DevAwake time.Duration
	// PublicOrigin is the dashboard's origin (scheme://host[:port]) that a
	// cookie request's Origin must match; empty: the request's own scheme
	// and Host.
	PublicOrigin string
	// CookieInsecure leaves Secure off the session cookie, for a dev stack
	// on plain http only.
	CookieInsecure bool
	// TrustedProxies are the proxy addresses or CIDR ranges whose
	// X-Forwarded-For names the client; none: the socket's address.
	TrustedProxies []string
	// YTDLP is the yt-dlp program the yt-dlp player runs (CADENCE_YTDLP); empty
	// when the server has none, so no yt-dlp instance builds.
	YTDLP string
	// WakeWordNegatives is the file the custom wake word's training reads its negative features from
	// (CADENCE_WAKEWORD_NEGATIVES): openWakeWord's CC BY-NC-SA features, which the server fetches there on the owner's
	// ask and never ships; empty, training is refused
	WakeWordNegatives string
	// AmbientManifest is where the screens' Aerial manifest is fetched from (CADENCE_AMBIENT_MANIFEST): empty for
	// Apple's (none beside the fakes, so the dev stack never reaches Apple), "off" for none, else an https url,
	// whose host alone it may come from (a fake Apple host)
	AmbientManifest string
}

// Error names the variable only, so a secret value never reaches a log.
type Error struct{ Variable, Reason string }

func (e Error) Error() string { return e.Variable + ": " + e.Reason }

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Listen:            getenv("CADENCE_LISTEN"),
		DatabaseURL:       getenv("DATABASE_URL"),
		MasterKeyFile:     getenv("CADENCE_MASTER_KEY_FILE"),
		DevFakes:          getenv("CADENCE_DEV_FAKES") == "1",
		YTDLP:             getenv("CADENCE_YTDLP"),
		WakeWordNegatives: getenv("CADENCE_WAKEWORD_NEGATIVES"),
	}
	if s := getenv("CADENCE_AMBIENT_MANIFEST"); s != "" {
		if u, err := url.Parse(s); s != "off" && (err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil) {
			return Config{}, Error{Variable: "CADENCE_AMBIENT_MANIFEST", Reason: "must be off or an https url"}
		}
		c.AmbientManifest = s
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.DatabaseURL == "" {
		return Config{}, Error{Variable: "DATABASE_URL", Reason: "required"}
	}
	if !strings.HasPrefix(c.DatabaseURL, "postgres://") && !strings.HasPrefix(c.DatabaseURL, "postgresql://") {
		return Config{}, Error{Variable: "DATABASE_URL", Reason: "must be a postgres url"}
	}
	if c.MasterKeyFile == "" {
		return Config{}, Error{Variable: "CADENCE_MASTER_KEY_FILE", Reason: "required"}
	}
	if s := getenv("CADENCE_DEV_COMPACT_SECONDS"); c.DevFakes && s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 60 {
			return Config{}, Error{Variable: "CADENCE_DEV_COMPACT_SECONDS", Reason: "must be 1 to 60"}
		}
		c.DevCompactUnit = time.Duration(n) * time.Second
	}
	if s := getenv("CADENCE_DEV_AWAKE_SECONDS"); c.DevFakes && s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 120 {
			return Config{}, Error{Variable: "CADENCE_DEV_AWAKE_SECONDS", Reason: "must be 1 to 120"}
		}
		c.DevAwake = time.Duration(n) * time.Second
	}
	if s := getenv("CADENCE_PUBLIC_ORIGIN"); s != "" {
		if !isOrigin(s) {
			return Config{}, Error{Variable: "CADENCE_PUBLIC_ORIGIN", Reason: "must be an origin: http(s)://host[:port], lowercase, no default port, path or trailing slash"}
		}
		c.PublicOrigin = s
	}
	c.CookieInsecure = getenv("CADENCE_COOKIE_INSECURE") == "1"
	if s := getenv("CADENCE_TRUSTED_PROXY"); s != "" {
		for p := range strings.SplitSeq(s, ",") {
			p = strings.TrimSpace(p)
			if _, err := netip.ParsePrefix(p); err != nil {
				if _, err := netip.ParseAddr(p); err != nil {
					return Config{}, Error{Variable: "CADENCE_TRUSTED_PROXY", Reason: "must be comma-separated ip addresses or cidr ranges"}
				}
			}
			c.TrustedProxies = append(c.TrustedProxies, p)
		}
	}
	c.YandexProxy = YandexProxyConfig{
		Listen:      getenv("CADENCE_YANDEX_PROXY_LISTEN"),
		CACertFile:  getenv("CADENCE_YANDEX_PROXY_CA_CERT"),
		CAKeyFile:   getenv("CADENCE_YANDEX_PROXY_CA_KEY"),
		UpstreamDNS: getenv("CADENCE_YANDEX_PROXY_UPSTREAM_DNS"),
	}
	if c.YandexProxy.Listen != "" {
		if c.YandexProxy.CACertFile == "" {
			return Config{}, Error{Variable: "CADENCE_YANDEX_PROXY_CA_CERT", Reason: "required when the proxy listens"}
		}
		if c.YandexProxy.CAKeyFile == "" {
			return Config{}, Error{Variable: "CADENCE_YANDEX_PROXY_CA_KEY", Reason: "required when the proxy listens"}
		}
		clients := getenv("CADENCE_YANDEX_PROXY_CLIENTS")
		if clients == "" {
			return Config{}, Error{Variable: "CADENCE_YANDEX_PROXY_CLIENTS", Reason: "required when the proxy listens"}
		}
		for p := range strings.SplitSeq(clients, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(p))
			if err != nil {
				if addr, addrErr := netip.ParseAddr(strings.TrimSpace(p)); addrErr == nil {
					prefix = netip.PrefixFrom(addr, addr.BitLen())
				} else {
					return Config{}, Error{Variable: "CADENCE_YANDEX_PROXY_CLIENTS", Reason: "must be comma-separated ip addresses or cidr ranges"}
				}
			}
			c.YandexProxy.Clients = append(c.YandexProxy.Clients, prefix)
		}
		if dns := c.YandexProxy.UpstreamDNS; dns != "" {
			if _, err := netip.ParseAddrPort(dns); err != nil {
				return Config{}, Error{Variable: "CADENCE_YANDEX_PROXY_UPSTREAM_DNS", Reason: "must be an ip address and port"}
			}
		}
		if devices := getenv("CADENCE_YANDEX_PROXY_DEVICES"); devices != "" {
			c.YandexProxy.Devices = map[string]string{}
			for entry := range strings.SplitSeq(devices, ",") {
				vendor, cadence, ok := strings.Cut(strings.TrimSpace(entry), "=")
				id, err := uuid.Parse(cadence)
				if !ok || !validVendorID(vendor) || err != nil {
					return Config{}, Error{Variable: "CADENCE_YANDEX_PROXY_DEVICES", Reason: "must be comma-separated vendor-device-id=cadence-device-uuid pairs"}
				}
				c.YandexProxy.Devices[vendor] = id.String()
			}
		}
	}
	return c, nil
}

func validVendorID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if r != '-' && r != '_' && r != '.' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// isOrigin holds for an origin as a browser serializes it in an Origin header,
// so the two compare as strings.
func isOrigin(s string) bool {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || s != u.Scheme+"://"+u.Host {
		return false
	}
	if s != strings.ToLower(s) || (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		return false
	}
	return true
}
