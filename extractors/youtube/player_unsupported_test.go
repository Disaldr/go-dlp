package youtube

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	ex "github.com/Disaldr/go-dlp/extractor"
)

// Регрессия на пункт «чёткая ошибка при смене плеера YouTube»: если base.js
// скачался, но функцию расшифровки подписи в нём найти/скомпилировать не
// удалось (формат плеера изменился), loadPlayer должен возвращать ошибку,
// распознаваемую как ex.ErrPlayerUnsupported. Сетевой сбой так помечаться
// не должен — это не повод обновлять экстрактор.
func TestLoadPlayerClassifiesErrors(t *testing.T) {
	cases := []struct {
		name            string
		status          int
		body            string
		wantErr         bool
		wantUnsupported bool
	}{
		{
			name:            "valid player",
			status:          200,
			body:            miniPlayer,
			wantErr:         false,
			wantUnsupported: false,
		},
		{
			name:   "changed player without signature function",
			status: 200,
			// Валидный JS, но ни es6-маркера, ни классической функции подписи:
			// имитирует смену формата base.js.
			body:            `var _yt_player={};var foo=function(a){return a+1};foo(2);`,
			wantErr:         true,
			wantUnsupported: true,
		},
		{
			name:            "network error is not a player-version problem",
			status:          500,
			body:            "oops",
			wantErr:         true,
			wantUnsupported: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			env := &ex.Env{HTTP: srv.Client()}
			_, err := loadPlayer(context.Background(), env, srv.URL+"/s/player/new/base.js")
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got := errors.Is(err, ex.ErrPlayerUnsupported); got != tc.wantUnsupported {
				t.Fatalf("errors.Is(err, ErrPlayerUnsupported) = %v, want %v (err: %v)", got, tc.wantUnsupported, err)
			}
		})
	}
}
