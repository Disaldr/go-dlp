package youtube

// Авторизация запросов innertube по cookies аккаунта Google (см.
// _get_sid_authorization_header в yt-dlp). Браузер подписывает запросы к
// youtubei заголовком Authorization: SAPISIDHASH ..., вычисляемым из cookie
// SAPISID; без него cookies сессии API не принимает и отвечает как гостю.

import (
	"crypto/sha1"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	ex "github.com/Disaldr/go-dlp/extractor"
)

// auth — данные залогиненной сессии для подписи запросов.
type auth struct {
	sapisid, sid1p, sid3p string
	userSessionID         string // из DATASYNC_ID, участвует в хеше
	delegatedSessionID    string // brand-аккаунт → X-Goog-PageId
	sessionIndex          string // X-Goog-AuthUser
}

// sessionAuth возвращает данные авторизации, если в cookies есть SAPISID
// (или __Secure-3PAPISID), иначе nil.
func sessionAuth(env *ex.Env, ytcfg any) *auth {
	a := &auth{
		sapisid: env.Cookie(baseURL, "SAPISID"),
		sid1p:   env.Cookie(baseURL, "__Secure-1PAPISID"),
		sid3p:   env.Cookie(baseURL, "__Secure-3PAPISID"),
	}
	if a.sapisid == "" {
		a.sapisid = a.sid3p
	}
	if a.sapisid == "" {
		return nil
	}
	// DATASYNC_ID: "DELEGATED||USER" для brand-аккаунта или "USER||".
	parseDatasync(a, ex.FirstStr(ytcfg, ex.P("DATASYNC_ID"), ex.P("datasyncId")))
	if s := ex.FirstStr(ytcfg, ex.P("USER_SESSION_ID")); s != "" {
		a.userSessionID = s
	}
	a.sessionIndex = "0"
	if v := ex.Get(ytcfg, "SESSION_INDEX"); v != nil {
		switch v := v.(type) {
		case string:
			if v != "" {
				a.sessionIndex = v
			}
		case float64:
			a.sessionIndex = strconv.Itoa(int(v))
		}
	}
	return a
}

func parseDatasync(a *auth, ds string) {
	if ds == "" {
		return
	}
	parts := strings.Split(ds, "||")
	if len(parts) > 1 && parts[1] != "" {
		a.delegatedSessionID, a.userSessionID = parts[0], parts[1]
	} else {
		a.userSessionID = parts[0]
	}
}

// header возвращает значение Authorization для origin в момент now.
func (a *auth) header(origin string, now time.Time) string {
	ts := strconv.FormatInt(now.Unix(), 10)
	parts := []string{sidAuthorization("SAPISIDHASH", a.sapisid, origin, ts, a.userSessionID)}
	if a.sid1p != "" {
		parts = append(parts, sidAuthorization("SAPISID1PHASH", a.sid1p, origin, ts, a.userSessionID))
	}
	if a.sid3p != "" {
		parts = append(parts, sidAuthorization("SAPISID3PHASH", a.sid3p, origin, ts, a.userSessionID))
	}
	return strings.Join(parts, " ")
}

// sidAuthorization: "<scheme> <ts>_<sha1([u ]ts sid origin)>[_u]".
func sidAuthorization(scheme, sid, origin, ts, userSessionID string) string {
	var in []string
	if userSessionID != "" {
		in = append(in, userSessionID)
	}
	in = append(in, ts, sid, origin)
	sum := sha1.Sum([]byte(strings.Join(in, " ")))
	out := scheme + " " + ts + "_" + hex.EncodeToString(sum[:])
	if userSessionID != "" {
		out += "_u"
	}
	return out
}

// apply добавляет заголовки авторизованного запроса.
func (a *auth) apply(h map[string]string) {
	h["Authorization"] = a.header(baseURL, time.Now())
	h["X-Origin"] = baseURL
	h["X-Goog-AuthUser"] = a.sessionIndex
	h["X-Youtube-Bootstrap-Logged-In"] = "true"
	if a.delegatedSessionID != "" {
		h["X-Goog-PageId"] = a.delegatedSessionID
	}
}
