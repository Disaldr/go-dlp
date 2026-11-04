package youtube

import (
	"testing"
	"time"
)

func TestSidAuthorization(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// Эталоны: printf '%s' "<вход>" | shasum -a 1
	a := &auth{sapisid: "abcSID"}
	if got, want := a.header(baseURL, now), "SAPISIDHASH 1700000000_c1a82c8415b40302296254ac25a1c8824a715464"; got != want {
		t.Errorf("no session id:\n got %s\nwant %s", got, want)
	}
	a = &auth{sapisid: "abcSID", sid3p: "sid3", userSessionID: "106818"}
	want := "SAPISIDHASH 1700000000_b7a54b2b8f23bd44ecf134cb5d03d1c5fb22d117_u " +
		"SAPISID3PHASH 1700000000_51bd07efa4146e97d7602416e754be107c38cfd4_u"
	if got := a.header(baseURL, now); got != want {
		t.Errorf("with session id:\n got %s\nwant %s", got, want)
	}
}

func TestSessionAuthDatasync(t *testing.T) {
	for _, tc := range []struct{ ds, user, delegated string }{
		{"106818||", "106818", ""},
		{"777||106818", "106818", "777"},
	} {
		a := &auth{}
		parseDatasync(a, tc.ds)
		if a.userSessionID != tc.user || a.delegatedSessionID != tc.delegated {
			t.Errorf("%q: user=%q delegated=%q", tc.ds, a.userSessionID, a.delegatedSessionID)
		}
	}
}
