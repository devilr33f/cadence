package xweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
	"git.shino.boo/devilreef/cadence/server/internal/providers/httpx"
)

const (
	// MaxLimit bounds one search's posts
	MaxLimit = 20
	// how much of a post, a quoted one and a note reach the model
	maxTextRunes  = 800
	maxQuoteRunes = 300
	maxNoteRunes  = 500
)

func isRedirect(err error) bool { return errors.Is(err, httpx.ErrRedirect) }

func (c *Client) searchOnce(ctx context.Context, s *session, query string, latest bool, limit int) ([]providers.Post, error) {
	product := "Top"
	if latest {
		product = "Latest"
	}
	vars, _ := json.Marshal(map[string]any{
		"rawQuery": query, "count": limit, "querySource": "typed_query", "product": product,
	})
	path := "/i/api/graphql/" + s.queryID + "/SearchTimeline"
	q := url.Values{"variables": {string(vars)}, "features": {s.features}, "fieldToggles": {s.fieldToggles}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, &failure{code: "request"}
	}
	seconds := (c.cfg.Now().UnixMilli() - txEpochSeconds*1000) / 1000
	h := req.Header
	h.Set("User-Agent", userAgent)
	h.Set("Accept", "*/*")
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer "+s.bearer)
	h.Set("Cookie", c.cookieHeader())
	h.Set("X-Csrf-Token", c.csrfToken())
	h.Set("X-Twitter-Auth-Type", "OAuth2Session")
	h.Set("X-Twitter-Active-User", "yes")
	h.Set("X-Twitter-Client-Language", "en")
	h.Set("X-Client-Transaction-Id", s.signer.id(http.MethodGet, path, seconds, randomByte()))
	h.Set("Referer", c.cfg.BaseURL+"/search?q="+url.QueryEscape(query)+"&src=typed_query")
	resp, err := c.http.Do(req)
	if err != nil {
		if isRedirect(err) {
			return nil, &failure{code: "signed_out"}
		}
		return nil, sendFailure(ctx)
	}
	defer func() { _ = resp.Body.Close() }()
	c.absorb(resp)
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &failure{code: "rate_limited"}
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, &failure{code: "signed_out", reset: true}
	// a 404 is a query id x retired, a 400 features it changed, a 403 a signature it no longer takes: all read again
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusForbidden:
		return nil, &failure{code: statusCode("status_", resp.StatusCode), reset: true}
	case resp.StatusCode != http.StatusOK:
		return nil, &failure{code: statusCode("status_", resp.StatusCode)}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, sendFailure(ctx)
	}
	posts, ok := parseSearch(b, limit)
	if !ok {
		return nil, &failure{code: "decode"}
	}
	return posts, nil
}

// the slice of x's tweet result this reads
type tweetResult struct {
	Typename string       `json:"__typename"`
	Tweet    *tweetResult `json:"tweet"`
	RestID   string       `json:"rest_id"`
	Core     struct {
		UserResults struct {
			Result struct {
				Core struct {
					Name       string `json:"name"`
					ScreenName string `json:"screen_name"`
				} `json:"core"`
				Legacy struct {
					Name       string `json:"name"`
					ScreenName string `json:"screen_name"`
				} `json:"legacy"`
			} `json:"result"`
		} `json:"user_results"`
	} `json:"core"`
	Views struct {
		Count string `json:"count"`
	} `json:"views"`
	NoteTweet struct {
		Results struct {
			Result struct {
				Text      string   `json:"text"`
				EntitySet entities `json:"entity_set"`
			} `json:"result"`
		} `json:"note_tweet_results"`
	} `json:"note_tweet"`
	Birdwatch struct {
		Subtitle struct {
			Text string `json:"text"`
		} `json:"subtitle"`
	} `json:"birdwatch_pivot"`
	Quoted struct {
		Result *tweetResult `json:"result"`
	} `json:"quoted_status_result"`
	Legacy struct {
		FullText         string   `json:"full_text"`
		CreatedAt        string   `json:"created_at"`
		FavoriteCount    int      `json:"favorite_count"`
		RetweetCount     int      `json:"retweet_count"`
		ReplyCount       int      `json:"reply_count"`
		InReplyToScreen  string   `json:"in_reply_to_screen_name"`
		Entities         entities `json:"entities"`
		ExtendedEntities entities `json:"extended_entities"`
		RetweetedStatus  struct {
			Result *tweetResult `json:"result"`
		} `json:"retweeted_status_result"`
	} `json:"legacy"`
}

type entities struct {
	URLs []struct {
		URL         string `json:"url"`
		ExpandedURL string `json:"expanded_url"`
	} `json:"urls"`
	Media []struct {
		URL string `json:"url"`
	} `json:"media"`
}

type entry struct {
	EntryID string `json:"entryId"`
	Content struct {
		ItemContent *struct {
			TweetResults struct {
				Result *tweetResult `json:"result"`
			} `json:"tweet_results"`
		} `json:"itemContent"`
		Items []struct {
			Item struct {
				ItemContent *struct {
					TweetResults struct {
						Result *tweetResult `json:"result"`
					} `json:"tweet_results"`
				} `json:"itemContent"`
			} `json:"item"`
		} `json:"items"`
	} `json:"content"`
}

type searchResponse struct {
	Data *struct {
		SearchByRawQuery struct {
			SearchTimeline struct {
				Timeline struct {
					Instructions []struct {
						Type    string  `json:"type"`
						Entries []entry `json:"entries"`
						Entry   *entry  `json:"entry"`
					} `json:"instructions"`
				} `json:"timeline"`
			} `json:"search_timeline"`
		} `json:"search_by_raw_query"`
	} `json:"data"`
}

// parseSearch reads the posts of a SearchTimeline answer in x's order, promoted ones left out; ok is false for a
// body that is not one
func parseSearch(b []byte, limit int) ([]providers.Post, bool) {
	var r searchResponse
	if json.Unmarshal(b, &r) != nil || r.Data == nil {
		return nil, false
	}
	var results []*tweetResult
	add := func(e entry) {
		if strings.HasPrefix(e.EntryID, "promoted") {
			return
		}
		if ic := e.Content.ItemContent; ic != nil && ic.TweetResults.Result != nil {
			results = append(results, ic.TweetResults.Result)
		}
		for _, it := range e.Content.Items {
			if ic := it.Item.ItemContent; ic != nil && ic.TweetResults.Result != nil {
				results = append(results, ic.TweetResults.Result)
			}
		}
	}
	for _, in := range r.Data.SearchByRawQuery.SearchTimeline.Timeline.Instructions {
		for _, e := range in.Entries {
			add(e)
		}
		if in.Entry != nil {
			add(*in.Entry)
		}
	}
	out := []providers.Post{}
	for _, t := range results {
		if p, ok := toPost(t, maxTextRunes, true); ok {
			out = append(out, p)
			if len(out) == limit {
				break
			}
		}
	}
	return out, true
}

func unwrap(t *tweetResult) *tweetResult {
	if t != nil && t.Tweet != nil && t.Typename == "TweetWithVisibilityResults" {
		return t.Tweet
	}
	return t
}

func handleOf(t *tweetResult) (name, handle string) {
	u := t.Core.UserResults.Result
	name, handle = u.Core.Name, u.Core.ScreenName
	if handle == "" {
		name, handle = u.Legacy.Name, u.Legacy.ScreenName
	}
	return name, handle
}

// toPost turns a result into a post: a repost into the post it reposts, with who reposted it
func toPost(raw *tweetResult, textRunes int, withQuote bool) (providers.Post, bool) {
	t := unwrap(raw)
	if t == nil || t.RestID == "" {
		return providers.Post{}, false
	}
	var repostedBy string
	if rt := unwrap(t.Legacy.RetweetedStatus.Result); rt != nil && rt.RestID != "" {
		if _, h := handleOf(t); h != "" {
			repostedBy = "@" + h
		}
		t = rt
	}
	name, handle := handleOf(t)
	text, ents := t.Legacy.FullText, t.Legacy.Entities
	// a long post's whole body is in its note tweet; full_text stops at 280 characters
	if long := t.NoteTweet.Results.Result.Text; long != "" {
		text, ents = long, t.NoteTweet.Results.Result.EntitySet
	}
	p := providers.Post{
		Author: name, Text: clip(expand(text, ents, t.Legacy.ExtendedEntities), textRunes),
		Likes: t.Legacy.FavoriteCount, Reposts: t.Legacy.RetweetCount, Replies: t.Legacy.ReplyCount,
		Views: atoi(t.Views.Count), RepostedBy: repostedBy, Note: clip(strings.TrimSpace(t.Birdwatch.Subtitle.Text), maxNoteRunes),
	}
	if handle != "" {
		p.Handle = "@" + handle
		p.URL = "https://x.com/" + handle + "/status/" + t.RestID
	} else {
		p.URL = "https://x.com/i/status/" + t.RestID
	}
	if t.Legacy.InReplyToScreen != "" {
		p.ReplyTo = "@" + t.Legacy.InReplyToScreen
	}
	if ts, err := time.Parse(time.RubyDate, t.Legacy.CreatedAt); err == nil {
		p.Time = ts.UTC().Format(time.RFC3339)
	}
	if withQuote {
		if q, ok := toPost(t.Quoted.Result, maxQuoteRunes, false); ok {
			p.Quote = &q
		}
	}
	return p, true
}

// expand swaps t.co links for where they go and drops the trailing link of attached media
func expand(text string, e, extended entities) string {
	for _, u := range e.URLs {
		if u.URL != "" && u.ExpandedURL != "" {
			text = strings.ReplaceAll(text, u.URL, u.ExpandedURL)
		}
	}
	for _, m := range append(e.Media, extended.Media...) {
		if m.URL != "" {
			text = strings.ReplaceAll(text, m.URL, "")
		}
	}
	return strings.TrimSpace(strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(text))
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
