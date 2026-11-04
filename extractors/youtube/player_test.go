package youtube

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	ex "github.com/Disaldr/go-dlp/extractor"
)

// miniPlayer — компактный base.js с теми же конструкциями, что и настоящий
// плеер: объект-помощник, функция дешифровки подписи и функция n-параметра.
const miniPlayer = `
var _yt_player={};
var Wx={
 XX:function(a,b){a.reverse()},
 YY:function(a,b){var c=a[0];a[0]=a[b%a.length];a[b%a.length]=c},
 ZZ:function(a,b){a.splice(0,b)}
};
var Qq=function(a){a=a.split("");Wx.XX(a,0);Wx.ZZ(a,2);Wx.YY(a,5);return a.join("")};
g.sig||(c=Qq(decodeURIComponent(c)));
var Nf=function(a){var b=a.split(""),c=0;b.reverse();return b.join("")};
a.set("n",Nf(a.get("n")));
`

func TestNewPlayerDecrypt(t *testing.T) {
	p, err := newPlayer(miniPlayer)
	if err != nil {
		t.Fatalf("newPlayer: %v", err)
	}
	// split → reverse → splice(0,2) → swap(0,5) → join (см. miniPlayer).
	got, err := p.decryptSig("abcdefgh")
	if err != nil {
		t.Fatalf("decryptSig: %v", err)
	}
	if got != "aedcbf" {
		t.Errorf("decryptSig = %q, want %q", got, "aedcbf")
	}
	if !p.hasN {
		t.Fatal("n-transform function was not extracted")
	}
	if out, ok := p.decryptN("xyz"); !ok || out != "zyx" {
		t.Errorf("decryptN = %q, %v; want zyx, true", out, ok)
	}
}

func TestDecipherURL(t *testing.T) {
	p, err := newPlayer(miniPlayer)
	if err != nil {
		t.Fatal(err)
	}
	// url-encoded cipher: подпись s под именем параметра sp=sig.
	cipher := "s=abcdefgh&sp=sig&url=https%3A%2F%2Fr1.googlevideo.com%2Fvideoplayback%3Fitag%3D18"
	got, err := decipherURL(p, cipher)
	if err != nil {
		t.Fatalf("decipherURL: %v", err)
	}
	if !strings.Contains(got, "sig=aedcbf") {
		t.Errorf("decipherURL = %q, want sig=aedcbf", got)
	}
	if !strings.HasPrefix(got, "https://r1.googlevideo.com/videoplayback?") {
		t.Errorf("decipherURL lost base URL: %q", got)
	}
	// Без плеера — явная ошибка, а не паника.
	if _, err := decipherURL(nil, cipher); err == nil {
		t.Error("expected error without player")
	}
	// Шифр без обязательных полей отвергается.
	if _, err := decipherURL(p, "sp=sig"); err == nil {
		t.Error("expected error for cipher without url/s")
	}
}

func TestApplyNTransform(t *testing.T) {
	p, err := newPlayer(miniPlayer)
	if err != nil {
		t.Fatal(err)
	}
	in := "https://r1.googlevideo.com/videoplayback?itag=18&n=xyz"
	out := applyNTransform(stubEnv(), p, in)
	if !strings.Contains(out, "n=zyx") {
		t.Errorf("applyNTransform = %q, want n=zyx", out)
	}
	// URL без n-параметра остаётся как есть.
	noN := "https://r1.googlevideo.com/videoplayback?itag=18"
	if out := applyNTransform(stubEnv(), p, noN); out != noN {
		t.Errorf("applyNTransform changed URL without n: %q", out)
	}
	// Без плеера URL не меняется.
	if out := applyNTransform(stubEnv(), nil, in); out != in {
		t.Errorf("applyNTransform without player changed URL: %q", out)
	}
}

func TestExtractFunctionBraces(t *testing.T) {
	// Строки и шаблоны со скобками не должны сбивать баланс.
	code := `var zz=function(a){var s="}}}";var t=` + "`${a}}`" + `;return s+t+"{"};tail`
	body, args, err := extractFunction(code, "zz")
	if err != nil {
		t.Fatalf("extractFunction: %v", err)
	}
	if args != "a" {
		t.Errorf("args = %q, want a", args)
	}
	if !strings.Contains(body, `return s+t+"{"`) || strings.Contains(body, "tail") {
		t.Errorf("body not balanced correctly: %q", body)
	}
}

func TestPlayerURL(t *testing.T) {
	ytcfg := map[string]any{"PLAYER_JS_URL": "/s/player/abcd1234/player_ias.vflset/en_US/base.js"}
	if got := playerURL(ytcfg, ""); got != baseURL+"/s/player/abcd1234/player_ias.vflset/en_US/base.js" {
		t.Errorf("playerURL from ytcfg = %q", got)
	}
	// Фолбэк на jsUrl со страницы с экранированными слэшами.
	page := `...,"jsUrl":"\/s\/player\/deadbeef\/base.js",...`
	if got := playerURL(nil, page); got != baseURL+"/s/player/deadbeef/base.js" {
		t.Errorf("playerURL from page = %q", got)
	}
	if got := playerURL(nil, "no url here"); got != "" {
		t.Errorf("playerURL with nothing = %q, want empty", got)
	}
}

func TestExtractNFunctionArrayIndex(t *testing.T) {
	// Имя n-функции спрятано в массиве: c=Xi[0](c).
	code := `
var Rn=function(a){var b=a.split(""),c=0;b.reverse();return b.join("")};
var Xi=[Rn];
c=Xi[0](c);
`
	src, name, err := extractNFunction(code)
	if err != nil {
		t.Fatalf("extractNFunction: %v", err)
	}
	if name != "Rn" {
		t.Errorf("n func name = %q, want Rn", name)
	}
	if !strings.Contains(src, "function Rn(a)") {
		t.Errorf("n func src = %q", src)
	}
}

func stubEnv() *ex.Env { return &ex.Env{} }

// TestRealPlayer проверяет извлечение на настоящем base.js.
// Включается указанием пути: GODLP_BASEJS=/path/to/base.js go test ./extractors/youtube.
//
// Для точной проверки можно задать ожидаемые значения (эталон — yt-dlp или
// ejs-solver): GODLP_SIG_IN/GODLP_SIG_OUT и GODLP_N_IN/GODLP_N_OUT. При их
// наличии результат сверяется побайтово; иначе проверяется лишь то, что
// преобразование вообще произошло.
func TestRealPlayer(t *testing.T) {
	path := os.Getenv("GODLP_BASEJS")
	if path == "" {
		t.Skip("set GODLP_BASEJS=/path/to/base.js to run")
	}
	code, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newPlayer(string(code))
	if err != nil {
		t.Fatalf("newPlayer on real base.js: %v", err)
	}
	t.Logf("combined(es6)=%v hasN=%v", p.combined, p.hasN)

	// --- подпись ---
	sigIn := os.Getenv("GODLP_SIG_IN")
	if sigIn == "" {
		sigIn = "AAJfQdSswRgIhAIxfakefakefakefakefakefakefakefakefakefakefakefake0123456789AAAAAAAA"
	}
	out, err := p.decryptSig(sigIn)
	if err != nil {
		t.Fatalf("decryptSig: %v", err)
	}
	if out == "" || out == sigIn {
		t.Errorf("decryptSig produced no transform: %q", out)
	}
	if want := os.Getenv("GODLP_SIG_OUT"); want != "" && out != want {
		t.Errorf("decryptSig(%q) = %q, want %q", sigIn, out, want)
	} else {
		t.Logf("decryptSig(%q) = %q", sigIn, out)
	}

	// --- n-параметр ---
	if !p.hasN {
		t.Fatal("n-transform not available on real player")
	}
	nIn := os.Getenv("GODLP_N_IN")
	if nIn == "" {
		nIn = "5hhEOA2Dj3aB8xZq"
	}
	n, ok := p.decryptN(nIn)
	if !ok {
		t.Fatal("decryptN failed on real player")
	}
	if want := os.Getenv("GODLP_N_OUT"); want != "" && n != want {
		t.Errorf("decryptN(%q) = %q, want %q", nIn, n, want)
	} else {
		t.Logf("decryptN(%q) = %q", nIn, n)
	}
}

// TestRealPlayerDownload — функциональная проверка: расшифровка реальной
// signatureCipher настоящим плеером и диапазонный GET первых байт (ожидается
// 200/206; неверные sig/n дают 403). Включается заданием пути к base.js и
// самой signatureCipher (сырое значение из streamingData, клиент web/tv):
//
//	GODLP_BASEJS=/path/base.js GODLP_CIPHER='s=...&sp=sig&url=...' go test -run TestRealPlayerDownload
func TestRealPlayerDownload(t *testing.T) {
	path := os.Getenv("GODLP_BASEJS")
	cipher := os.Getenv("GODLP_CIPHER")
	if cf := os.Getenv("GODLP_CIPHER_FILE"); cipher == "" && cf != "" {
		b, err := os.ReadFile(cf)
		if err != nil {
			t.Fatal(err)
		}
		cipher = strings.TrimSpace(string(b))
	}
	if path == "" || cipher == "" {
		t.Skip("set GODLP_BASEJS and GODLP_CIPHER (or GODLP_CIPHER_FILE) to run")
	}
	code, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newPlayer(string(code))
	if err != nil {
		t.Fatalf("newPlayer: %v", err)
	}
	u, err := decipherURL(p, cipher)
	if err != nil {
		t.Fatalf("decipherURL: %v", err)
	}
	u = applyNTransform(stubEnv(), p, u)
	t.Logf("deciphered URL: %.120s...", u)

	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Range", "bytes=0-1023")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	t.Logf("HTTP %d, %d bytes", resp.StatusCode, n)
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		t.Fatalf("expected 200/206, got %d (sig/n likely wrong)", resp.StatusCode)
	}
}
