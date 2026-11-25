package yandexmusic

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // the legacy link's signature is an md5, as yandex checks it
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

const (
	// get-file-info's sign key, of the desktop client musicClient names
	fileInfoKey = "kzqU4XhfCaY6B6JTHODeq5"
	// the legacy get-mp3 link's salt
	mp3Salt   = "XGRlBW9FXlekgbPrRHuSiA"
	transport = "raw"
)

// the codecs asked for, best first: every one the device decodes
var codecs = map[string]string{
	"hq":       "aac-mp4,aac,mp3,he-aac,he-aac-mp4",
	"nq":       "aac-mp4,aac,mp3,he-aac,he-aac-mp4",
	"lossless": "flac,flac-mp4,aac-mp4,aac,mp3,he-aac,he-aac-mp4",
}

// StreamTrack answers a stream of a song this provider found, from
// get-file-info, else the legacy download info. Without an account's token
// Yandex plays only a 30 s preview, so it is ErrSignInRequired at once, as is
// a refused token or one without a subscription; a song gone or only on an
// encrypted transport is ErrUnplayable.
func (c *Client) StreamTrack(ctx context.Context, t providers.Track) (providers.Stream, error) {
	if t.Source != providers.SourceYandex || !idRe.MatchString(t.ID) {
		return providers.Stream{}, providers.ErrUnplayable
	}
	if c.cfg.Token == "" && len(c.cfg.Cookies) == 0 {
		return providers.Stream{}, providers.ErrSignInRequired
	}
	var out providers.Stream
	err := c.guard(func() error {
		var err error
		out, err = c.fileInfo(ctx, t.ID)
		if err != nil && !final(err) {
			out, err = c.legacy(ctx, t.ID)
		}
		return err
	})
	switch {
	case err == nil:
		out.DurationS = max(t.DurationS, 0)
		return out, nil
	case errors.Is(err, providers.ErrUnplayable), errors.Is(err, providers.ErrSignInRequired):
		return providers.Stream{}, err
	case authFailure(err):
		return providers.Stream{}, providers.ErrSignInRequired
	case errors.Is(err, statusError(http.StatusNotFound)):
		return providers.Stream{}, providers.ErrUnplayable
	}
	return providers.Stream{}, errors.New(providers.CodePlayerFailed)
}

// final is a get-file-info answer the legacy route would only repeat
func final(err error) bool {
	return authFailure(err) || errors.Is(err, providers.ErrUnplayable) || errors.Is(err, providers.ErrSignInRequired) ||
		errors.Is(err, statusError(http.StatusNotFound))
}

func authFailure(err error) bool {
	return errors.Is(err, statusError(http.StatusUnauthorized)) || errors.Is(err, statusError(http.StatusForbidden)) ||
		errors.Is(err, errNoAccount)
}

// sign is get-file-info's signature: HMAC-SHA256 of the request's values,
// the codecs without their commas, in base64 without padding
func sign(ts, trackID, quality, codecs string) string {
	mac := hmac.New(sha256.New, []byte(fileInfoKey))
	mac.Write([]byte(ts + trackID + quality + strings.ReplaceAll(codecs, ",", "") + transport))
	return strings.TrimRight(base64.StdEncoding.EncodeToString(mac.Sum(nil)), "=")
}

func (c *Client) fileInfo(ctx context.Context, id string) (providers.Stream, error) {
	quality := c.cfg.Quality
	cs, ok := codecs[quality]
	if !ok {
		quality, cs = "hq", codecs["hq"]
	}
	ts := strconv.FormatInt(c.cfg.Now().Unix(), 10)
	q := url.Values{
		"ts": {ts}, "trackId": {id}, "quality": {quality}, "codecs": {cs}, "transports": {transport},
		"sign": {sign(ts, id, quality, cs)},
	}
	var r struct {
		DownloadInfo struct {
			Quality   string   `json:"quality"`
			Transport string   `json:"transport"`
			Key       string   `json:"key"`
			URL       string   `json:"url"`
			URLs      []string `json:"urls"`
		} `json:"downloadInfo"`
	}
	if err := c.get(ctx, "/get-file-info", q, true, &r); err != nil {
		return providers.Stream{}, err
	}
	d := r.DownloadInfo
	switch {
	case d.Quality == "preview":
		return providers.Stream{}, providers.ErrSignInRequired
	case d.Transport != transport || d.Key != "":
		// an encrypted file the relay cannot decrypt
		return providers.Stream{}, providers.ErrUnplayable
	}
	for _, u := range append(d.URLs, d.URL) {
		if c.mediaURL(u) {
			return providers.Stream{URL: u}, nil
		}
	}
	return providers.Stream{}, errBadAnswer
}

// mediaURL says whether u is an https url on one of MediaHosts
func (c *Client) mediaURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.User != nil || len(u) > 4096 {
		return false
	}
	host := strings.ToLower(p.Hostname())
	if c.testHost != "" && host == c.testHost {
		return true
	}
	for _, suffix := range MediaHosts {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// legacy is the older download-info route: an mp3's info document, then the
// get-mp3 link its values sign
func (c *Client) legacy(ctx context.Context, id string) (providers.Stream, error) {
	var infos []struct {
		Codec   string `json:"codec"`
		Bitrate int    `json:"bitrateInKbps"`
		InfoURL string `json:"downloadInfoUrl"`
		Preview bool   `json:"preview"`
	}
	if err := c.get(ctx, "/tracks/"+id+"/download-info", nil, true, &infos); err != nil {
		return providers.Stream{}, err
	}
	best, bitrate, previews := "", 0, 0
	for _, in := range infos {
		switch {
		case in.Preview:
			previews++
		case in.Codec == "mp3" && in.Bitrate > bitrate && c.yandexURL(in.InfoURL):
			best, bitrate = in.InfoURL, in.Bitrate
		}
	}
	if best == "" {
		if previews > 0 {
			return providers.Stream{}, providers.ErrSignInRequired
		}
		return providers.Stream{}, providers.ErrUnplayable
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, best, nil)
	if err != nil {
		return providers.Stream{}, errBadAnswer
	}
	tok, _, err := c.accessToken(ctx, false)
	if err != nil {
		return providers.Stream{}, err
	}
	req.Header.Set("Authorization", "OAuth "+tok)
	req.Header.Set("User-Agent", userAgent)
	b, err := c.do(req)
	if err != nil {
		return providers.Stream{}, err
	}
	var x struct {
		Host string `xml:"host"`
		Path string `xml:"path"`
		TS   string `xml:"ts"`
		S    string `xml:"s"`
	}
	if xml.Unmarshal(b, &x) != nil || !strings.HasPrefix(x.Path, "/") || x.TS == "" || x.S == "" {
		return providers.Stream{}, errBadAnswer
	}
	u := "https://" + x.Host + "/get-mp3/" + mp3Sign(x.Path, x.S) + "/" + x.TS + x.Path
	if !c.mediaURL(u) || strings.ContainsAny(x.Host, "/?#@") {
		return providers.Stream{}, errBadAnswer
	}
	return providers.Stream{URL: u}, nil
}

func mp3Sign(path, s string) string {
	sum := md5.Sum([]byte(mp3Salt + path[1:] + s)) //nolint:gosec // yandex's own scheme, no secret of ours
	return hex.EncodeToString(sum[:])
}

// yandexURL says whether u is an https url of Yandex's, which may see the token
func (c *Client) yandexURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.User != nil {
		return false
	}
	h := strings.ToLower(p.Hostname())
	if c.testHost != "" && h == c.testHost {
		return true
	}
	return strings.HasSuffix(h, ".yandex.net") || strings.HasSuffix(h, ".yandex.ru")
}
