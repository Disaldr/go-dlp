package youtube

import (
	"net/url"
	"strings"
)

// PO Token (BotGuard). Сгенерировать его в goja нельзя (BotGuard требует
// полноценной браузерной среды), поэтому токен принимается извне — от GliGO,
// браузера или внешнего провайдера — через extractor-arg `po_token`, в
// формате yt-dlp:
//
//	--extractor-args "youtube:po_token=web.gvs+XXXX,web.player+YYYY"
//
// Контексты: gvs (потоки GoogleVideo, добавляется параметр pot к URL),
// player (запрос player API), subs (субтитры). Ключ без клиента
// («gvs+XXXX») применяется ко всем клиентам.
type poTokens struct{ m map[string]string }

func parsePoTokens(vals []string) poTokens {
	m := map[string]string{}
	for _, v := range vals {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			plus := strings.LastIndexByte(part, '+')
			if plus < 0 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(part[:plus]))
			tok := strings.TrimSpace(part[plus+1:])
			if key != "" && tok != "" {
				m[key] = tok
			}
		}
	}
	return poTokens{m}
}

// get возвращает токен для клиента и контекста (с запасным ключом без клиента).
func (p poTokens) get(client, context string) string {
	if t, ok := p.m[client+"."+context]; ok {
		return t
	}
	return p.m[context]
}

// any сообщает, задан ли для клиента хоть один токен (значит, запросы надо
// привязывать к visitor data).
func (p poTokens) any(client string) bool {
	for _, c := range []string{"player", "gvs", "subs"} {
		if p.get(client, c) != "" {
			return true
		}
	}
	return false
}

func (p poTokens) empty() bool { return len(p.m) == 0 }

// withPot добавляет к URL параметр pot=<token> (GVS PO Token).
func withPot(rawURL, tok string) string {
	if tok == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("pot", tok)
	u.RawQuery = q.Encode()
	return u.String()
}
