package youtube

// Client — конфигурация клиента innertube (см. INNERTUBE_CLIENTS в yt-dlp).
type Client struct {
	Name        string // ключ: "visionos", "tv" ...
	ClientName  string // INNERTUBE_CONTEXT.client.clientName
	Version     string
	ID          int // INNERTUBE_CONTEXT_CLIENT_NAME
	UserAgent   string
	DeviceMake  string
	DeviceModel string
	OSName      string
	OSVersion   string
	AndroidSDK  int
	// RequireJS — форматы зашифрованы и требуют JS-плеера (не поддерживается).
	RequireJS bool
	// RequirePOT — для HTTPS-форматов нужен PO Token.
	RequirePOT bool
	// Guest — не отправлять visitor data: YouTube выдаёт гостевой
	// playback-грант, снимающий требование PO Token (см. playerRequest).
	// Действует только без авторизации.
	Guest bool
	// SupportsCookies — клиент принимает авторизацию аккаунта (SAPISIDHASH).
	SupportsCookies bool
}

// Clients — известные клиенты. Значения синхронизированы с yt-dlp.
var Clients = map[string]*Client{
	"visionos": {
		Name: "visionos", ClientName: "VISIONOS", Version: "1.02", ID: 101,
		DeviceMake: "Apple", DeviceModel: "RealityDevice17,1",
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_7_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15",
		OSName:    "visionOS", OSVersion: "26.5.23O471",
	},
	"android_vr": {
		Name: "android_vr", ClientName: "ANDROID_VR", Version: "1.65.10", ID: 28,
		DeviceMake: "Oculus", DeviceModel: "Quest 3", AndroidSDK: 32,
		UserAgent: "com.google.android.apps.youtube.vr.oculus/1.65.10 (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip",
		OSName:    "Android", OSVersion: "12L", RequirePOT: true,
	},
	"ios": {
		Name: "ios", ClientName: "IOS", Version: "21.26.4", ID: 5,
		DeviceMake: "Apple", DeviceModel: "iPhone16,2",
		UserAgent: "com.google.ios.youtube/21.26.4 (iPhone16,2; U; CPU iOS 18_3_2 like Mac OS X;)",
		OSName:    "iPhone", OSVersion: "18.3.2.22D82", RequirePOT: true,
	},
	"tv": {
		Name: "tv", ClientName: "TVHTML5", Version: "7.20260707.07.00", ID: 7,
		UserAgent: "Mozilla/5.0 (ChromiumStylePlatform) Cobalt/25.lts.30.1034943-gold (unlike Gecko), Unknown_TV_Unknown_0/Unknown (Unknown, Unknown)",
		RequireJS: true, SupportsCookies: true,
	},
	// tv_simply — упрощённый TV-клиент: форматы с signatureCipher (goja),
	// без PO Token. Авторизацию не принимает (HTTP 400 с SAPISIDHASH).
	"tv_simply": {
		Name: "tv_simply", ClientName: "TVHTML5_SIMPLY", Version: "1.0", ID: 75,
		UserAgent: "Mozilla/5.0 (ChromiumStylePlatform) Cobalt/25.lts.30.1034943-gold (unlike Gecko), Unknown_TV_Unknown_0/Unknown (Unknown, Unknown)",
		RequireJS: true,
	},
	"web": {
		Name: "web", ClientName: "WEB", Version: "2.20260708.00.00", ID: 1,
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36,gzip(gfe)",
		RequireJS: true, RequirePOT: true, Guest: true, SupportsCookies: true,
	},
}

// DefaultClients — клиенты по умолчанию. visionos отдаёт готовые URL без
// расшифровки подписи и без загрузки плеера. Если он не дал форматов,
// пробуются FallbackClients. Состояние на 2026-11 (проверено вручную):
// tv отвечает UNPLAYABLE «The page needs to be reloaded» (и с авторизацией),
// web отдаёт только SABR; visionos, ios, android_vr и tv_simply отвечают
// HTTP 400 на заголовок SAPISIDHASH, поэтому авторизация (auth.go)
// применяется лишь к клиентам с SupportsCookies.
var DefaultClients = []string{"visionos"}

// FallbackClients — запрашиваются, если клиенты по умолчанию не дали
// форматов (бот-проверка, UNPLAYABLE). tv_simply не требует PO Token, его
// подписи и n расшифровываются в goja; цена — загрузка base.js.
var FallbackClients = []string{"tv_simply"}

func (c *Client) context() map[string]any {
	cl := map[string]any{
		"clientName":       c.ClientName,
		"clientVersion":    c.Version,
		"hl":               "en",
		"timeZone":         "UTC",
		"utcOffsetMinutes": 0,
	}
	if c.UserAgent != "" {
		cl["userAgent"] = c.UserAgent
	}
	if c.DeviceMake != "" {
		cl["deviceMake"] = c.DeviceMake
	}
	if c.DeviceModel != "" {
		cl["deviceModel"] = c.DeviceModel
	}
	if c.OSName != "" {
		cl["osName"] = c.OSName
	}
	if c.OSVersion != "" {
		cl["osVersion"] = c.OSVersion
	}
	if c.AndroidSDK != 0 {
		cl["androidSdkVersion"] = c.AndroidSDK
	}
	return map[string]any{"client": cl}
}
