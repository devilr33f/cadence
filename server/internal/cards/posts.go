package cards

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"git.shino.boo/devilreef/cadence/server/internal/providers"
)

// what an X search's card shows: at most maxPosts posts, none of them obvious
// spam or a promo, none a repeat, and none that is a bare link with nothing
// to read (an X article stays, named as one). The model still reads every
// post the tool found; this is the screen's view alone.

const (
	maxPosts = 3
	// a post on the card is a few lines; its panel shows it whole
	cardPostRunes  = 180
	cardQuoteRunes = 100
)

var (
	urlRe = regexp.MustCompile(`https?://[^\s]+`)
	// phrases no post worth reading on a tv carries: a sale, a giveaway, a
	// contact in direct messages or a messenger
	promoRe = regexp.MustCompile(`(?i)(\bdm (me|us|for|to)\b|\bairdrop\b|\bgiveaway\b|\bpromo ?code\b|\buse code\b|` +
		`\bwhatsapp\b|t\.me/|\bbuy followers\b|\bonlyfans\b|\bbulk\b.{0,40}\b(procurement|orders?)\b|` +
		`\blink in bio\b|\bfree crypto\b|промокод|розыгрыш|пиши(те)? в (лс|личку|директ)|заработ(ок|ай)|` +
		`подписывай(ся|тесь) на канал)`)
	hashtagRe = regexp.MustCompile(`[#$][\p{L}\p{N}_]+`)
)

// keptPosts filters, dedupes and caps what the card shows, in the search's order
func keptPosts(posts []providers.Post) []providers.Post {
	var kept []providers.Post
	seen := map[string]bool{}
	for _, p := range posts {
		if strings.TrimSpace(p.Text) == "" || (p.Author == "" && p.Handle == "") {
			continue
		}
		if spam(p.Text) || (onlyLinks(p.Text) && !xArticle(p.Text)) {
			continue
		}
		key := dedupeKey(p)
		if seen[key] || (p.URL != "" && seen[p.URL]) {
			continue
		}
		seen[key] = true
		if p.URL != "" {
			seen[p.URL] = true
		}
		kept = append(kept, p)
		if len(kept) == maxPosts {
			break
		}
	}
	return kept
}

// spam is an obvious promo: its phrases, a wall of hashtags or cashtags,
// shouting, or a run of emoji bullets
func spam(text string) bool {
	if promoRe.MatchString(text) || len(hashtagRe.FindAllString(text, -1)) >= 5 {
		return true
	}
	letters, upper, symbols := 0, 0, 0
	for _, r := range urlRe.ReplaceAllString(text, " ") {
		switch {
		case unicode.IsLetter(r):
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		case r >= 0x2190 && unicode.Is(unicode.So, r):
			// emoji, dingbats and arrows used as bullets
			symbols++
		}
	}
	if letters >= 40 && upper*10 >= letters*6 {
		return true
	}
	return symbols >= 6
}

// onlyLinks says the text is links and nothing else to read
func onlyLinks(text string) bool {
	rest := urlRe.ReplaceAllString(text, " ")
	for _, r := range rest {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return urlRe.MatchString(text)
}

func xArticle(text string) bool {
	for _, u := range urlRe.FindAllString(text, -1) {
		if p, err := url.Parse(u); err == nil && isX(p.Host) && strings.HasPrefix(p.Path, "/i/article/") {
			return true
		}
	}
	return false
}

func isX(host string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	return host == "x.com" || host == "twitter.com"
}

// dedupeKey is a post's words without links, case or spacing, its start alone
// (a repost of one text with another link is still the same)
func dedupeKey(p providers.Post) string {
	s := strings.ToLower(urlRe.ReplaceAllString(p.Text, " "))
	s = strings.Join(strings.Fields(s), " ")
	return "t:" + truncRunes(s, 120)
}

// cardText is a post's text as the card shows it: each link as its host
// (an X article as the article word), cut at a word with an ellipsis
func cardText(text string, runes int, article string) string {
	if onlyLinks(text) && xArticle(text) {
		return article
	}
	text = urlRe.ReplaceAllStringFunc(text, func(u string) string {
		p, err := url.Parse(u)
		if err != nil || p.Host == "" {
			return ""
		}
		if isX(p.Host) && strings.HasPrefix(p.Path, "/i/article/") {
			return article
		}
		host := strings.TrimPrefix(strings.ToLower(p.Host), "www.")
		if strings.Trim(p.Path, "/") != "" {
			return host + "/…"
		}
		return host
	})
	return ellipsis(clip(text, 4*runes), runes)
}

// ellipsis cuts s to n runes at a word's end, with "…"
func ellipsis(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	cut := len(r)
	for i := len(r) - 1; i > n*2/3; i-- {
		if unicode.IsSpace(r[i]) {
			cut = i
			break
		}
	}
	return strings.TrimRightFunc(string(r[:cut]), func(c rune) bool { return unicode.IsSpace(c) || unicode.IsPunct(c) }) + "…"
}
