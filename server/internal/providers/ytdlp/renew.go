package ytdlp

import (
	"context"
	"os"
	"slices"
	"strings"
	"time"

	"git.shino.boo/devilreef/cadence/server/internal/providers/netscape"
)

// how long a save of the renewed jar may take: it runs after the run, apart from it
const saveTimeout = 10 * time.Second

// renew takes the jar yt-dlp wrote back to path as the next run's, and saves it, when a cookie's value changed. A
// file that is not a jar (a run killed while it wrote) is left alone. The caller holds cookieLock.
func (p *Player) renew(path string) {
	b, err := os.ReadFile(path) //nolint:gosec // the run's own temp file
	if err != nil {
		return
	}
	jar, err := netscape.Parse(string(b))
	if err != nil || jar == nil || sameCookies(jar, p.jar) {
		return
	}
	p.jar = jar
	p.cfg.Log.Info("ytdlp_cookies_renewed", "cookies", len(netscape.Cookies(jar)))
	if p.cfg.Save == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
		defer cancel()
		if err := p.cfg.Save(ctx, "cookies", string(jar)); err != nil {
			p.cfg.Log.Warn("ytdlp_cookies_save_failed")
			return
		}
		p.cfg.Log.Info("ytdlp_cookies_saved")
	}()
}

// sameCookies compares two jars by their cookies' domain, path, name and value: yt-dlp writes its own header and an
// expiry that moves, which alone is no news worth a save
func sameCookies(a, b []byte) bool {
	key := func(jar []byte) []string {
		var out []string
		for _, c := range netscape.Cookies(jar) {
			out = append(out, strings.Join([]string{c.Domain, c.Path, c.Name, c.Value}, "\t"))
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(key(a), key(b))
}
