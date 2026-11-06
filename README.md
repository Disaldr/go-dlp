# go-dlp

Загрузчик видео и аудио на Go — библиотека и CLI. Порт ключевой функциональности
[yt-dlp](https://github.com/yt-dlp/yt-dlp): экстракторы сайтов, язык выбора форматов,
HLS/DASH, слияние дорожек (ffmpeg **или** встроенный муксер на чистом Go), шаблоны имён файлов.

- **Библиотека в первую очередь**: всё, что умеет CLI, доступно из Go-кода.
- **Реактивный реестр экстракторов**: экстракторы можно добавлять, заменять и удалять на лету через канал.
- **35 сайтов**, плюс generic-экстрактор для прямых ссылок, m3u8/mpd и встраиваний
  (с имитацией браузера при анти-бот-заслоне — для онлайн-кинотеатров).
- **Имитация браузера**: TLS- и HTTP/2-отпечаток Chrome/Firefox/Safari (`--impersonate`).

## Установка

Готовые бинарники для macOS, Linux и Windows: [GitHub Releases](https://github.com/Disaldr/go-dlp/releases/latest).

```sh
go install github.com/Disaldr/go-dlp/cmd/go-dlp@latest
# или из исходников
git clone https://github.com/Disaldr/go-dlp && cd go-dlp && go build -o bin/go-dlp ./cmd/go-dlp
```

Требуется Go 1.26+. Слияние видео и аудио работает **без ffmpeg**: встроенный муксер на чистом Go
собирает MP4 (прогрессивный и фрагментированный) и WebM/MKV копированием дорожек, без перекодирования.
ffmpeg нужен для извлечения/конвертации аудио (`-x`), перепаковки HLS (MPEG-TS → mp4)
и как запасной муксер для нестандартных кодеков; для `-x` также нужен ffprobe;
если он есть в `PATH` (или задан `--ffmpeg-location`), по умолчанию используется он.

## CLI

```sh
go-dlp -F https://youtu.be/jNQXAC9IVRw               # список форматов
go-dlp https://youtu.be/jNQXAC9IVRw                  # скачать лучшее (видео+аудио со слиянием)
go-dlp -f "bv*[height<=720]+ba/b" URL                # выбор форматов, как в yt-dlp
go-dlp -o "%(uploader)s/%(title)s.%(ext)s" URL       # шаблон имени файла
go-dlp -j URL | jq .title                            # JSON без загрузки
go-dlp -x --audio-format mp3 URL                    # извлечь аудио через ffmpeg
go-dlp --remux-video mkv URL                        # перепаковать и одиночный формат, и результат слияния
go-dlp --playlist-end 5 -e "ytsearch10:golang"       # поиск, первые 5 названий
go-dlp -M 4 URL1 URL2 PLAYLIST_URL                   # 4 параллельные загрузки (URL и элементы плейлиста)
go-dlp --cookies cookies.txt URL                     # cookies: Netscape cookies.txt или JSON-экспорт
go-dlp --impersonate chrome URL                      # имитация браузера
go-dlp extractors                                    # список экстракторов
go-dlp --help                                        # все флаги
```

Прямые эфиры (HLS/DASH) записываются с live-края; `Ctrl+C` останавливает запись, скачанное сохраняется
и сливается. Повторный `Ctrl+C` — немедленный выход.

`-M, --concurrent-downloads N` качает до `N` видео одновременно пулом горутин — параллелятся и
отдельные URL из командной строки, и элементы одного плейлиста. Не путать с `-N,
--concurrent-fragments` (параллельные HLS/DASH-фрагменты **внутри одного** видео): их можно
сочетать. При `-M > 1` единая строка прогресса отключается (она рассчитана на одну активную
загрузку) — остаются построчные сообщения по каждому видео. Режимы вывода и симуляции (`-j`, `-F`,
`-g`, `-s` и т. п.) всегда последовательны. Пользовательский `--progress-template`
продолжает выводить отдельные строки и при параллельной загрузке.

Для приложений, запускающих загрузчик как процесс, поддерживаются аргументы заданий GLIGO:

```sh
go-dlp --ignore-config --no-playlist --newline --progress --no-colors --encoding utf-8 \
  -f 'bestvideo+bestaudio/best' -P downloads -o '%(title).180B [%(id)s].%(ext)s' \
  --windows-filenames --merge-output-format mkv --remux-video mkv \
  --print 'after_move:GLIGO_FILE:%(filepath)s' \
  --progress-template 'download:GLIGO_PROGRESS:%(progress)j' -- URL
```

Шаблон прогресса выводится в **stdout** отдельными строками. `progress` содержит
`status`, `downloaded_bytes`, `total_bytes`, `total_bytes_estimate`, `speed`, `eta`,
`elapsed`, `filename` и счётчики фрагментов; скорость — байт/с, время — секунды.
Неизвестные размеры — `null`. Поля видео доступны как `%(info.id)s` и т. п.
`after_move` выводится после успешной постобработки (также для уже готового файла),
`filepath` — абсолютный путь; при ошибке маркер файла не выводится.
`--print` повторяемый, включает тихий режим; `--progress` сохраняет прогресс в этом режиме.
Поддерживаемые стадии печати: `video` (по умолчанию), `before_dl`, `after_move`, `after_video`.
Только `--print video` подразумевает симуляцию; `--no-simulate` включает загрузку.
Шаблон прогресса поддерживает тип `download`; другие типы отклоняются с ошибкой.

`-x --audio-format best|mp3|m4a|opus|aac|alac|flac|vorbis|wav` извлекает только аудио.
Совпадающий кодек копируется, иначе ffmpeg конвертирует звук; `best` сохраняет исходный кодек.
`--remux-video` принимает один контейнер и копирует дорожки без перекодирования:
несовместимый кодек приводит к ошибке с сохранением исходника. Правила вида `aac>m4a/mkv`
пока не поддерживаются. Эти возможности также доступны через `dlp.Options` и `dlp.Request`
(`ExtractAudio`, `AudioFormat`, `RemuxVideo`, `WindowsFilenames`).

Поддерживаются `--socket-timeout` (ожидание сетевых данных, а не длительность загрузки),
`--extractor-retries` (повторы временных сетевых ошибок), `--no-warnings` и аргументы
экстрактора без значения, например `generic:impersonate`. `--ignore-config` принимается,
поскольку go-dlp не читает конфигурационные файлы; `--encoding` принимает только UTF-8.
`--js-runtimes RUNTIME[:PATH]` принимается для совместимости: внешний runtime не запускается,
go-dlp использует встроенный JS-движок.

## Использование как библиотеки

Возможности API (всё — из одного `*Client`):

- **Извлечение и подготовка**: `Extract` (метаданные и форматы), `Prepare` (выбор формата и имя файла без загрузки), `DownloadInfo` / `DownloadVideo` (загрузка, для плейлиста — всех элементов).
- **Обход плейлистов**: `Walk` разворачивает элементы по одному с колбэком и мягкой обработкой ошибок отдельного элемента.
- **Реактивный реестр экстракторов**: `Registry().Subscribe` / `Watch`, `extractor.Add` / `Remove` — добавление, замена и удаление экстракторов на лету, потокобезопасно.
- **Параметры на каждый вызов**: `Request` и методы `*With` (`ExtractWith`, `DownloadWith`, `DownloadInfoWith`, `DownloadVideoWith`, `PrepareWith`) — свои формат, каталог, колбэк прогресса и `NoPlaylist`; нулевые поля наследуются из `Options`. Один клиент можно гонять из нескольких горутин (параллельные загрузки).
- **Вариант клиента**: `Clone` — новые cookies/прокси/User-Agent/имитация без повторной компиляции экстракторов (дорогой части `New`).
- **Прямые ссылки на поток**: URL лучшего формата без скачивания; `CookieHeader(url)` отдаёт сессионные cookies для внешнего плеера.
- **Слияние без ffmpeg**: встроенный муксер на чистом Go (MP4/WebM/MKV копированием дорожек) либо ffmpeg.
- **Аудио и перепаковка**: `ExtractAudio` / `AudioFormat` (`-x`), `RemuxVideo`, `WindowsFilenames` через `Options` / `Request`.
- **Версия и обновление экстракторов**: `dlp.Version`, `ExtractorsVersion()`, `UpdateExtractors(ctx, cacheDir)`.
- **Типизированные ошибки**: распознавание причины сбоя (см. [Типы ошибок](#типы-ошибок)).

```go
package main

import (
	"context"
	"fmt"
	"log"

	dlp "github.com/Disaldr/go-dlp"
	"github.com/Disaldr/go-dlp/downloader"
)

func main() {
	ctx := context.Background()

	client, err := dlp.New(dlp.Options{
		Format:         "bv*[height<=1080]+ba/b",
		OutputDir:      "downloads",
		OutputTemplate: "%(title)s [%(id)s].%(ext)s",
		Progress: func(p downloader.Progress) {
			fmt.Printf("\r%s: %.1f%%", p.Filename, p.Percent())
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Только метаданные и форматы.
	info, err := client.Extract(ctx, "https://www.youtube.com/watch?v=jNQXAC9IVRw")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(info.Title, info.Duration, len(info.Formats))
	for _, f := range info.Formats {
		fmt.Println(f.FormatID, f.Ext, f.Resolution(), f.VCodec, f.ACodec)
	}

	// Выбор форматов и имя файла без загрузки.
	prepared, err := client.Prepare(info)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(prepared[0].FormatID, prepared[0].Filename)

	// Загрузка (для плейлиста — всех элементов).
	results, err := client.DownloadInfo(ctx, info)
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range results {
		fmt.Println(r.Filename, r.Err)
	}
}
```

Обход плейлиста с разворачиванием элементов:

```go
info, _ := client.Extract(ctx, "https://www.youtube.com/playlist?list=PLBCF2DAC6FFB574DE")
_ = client.Walk(ctx, info, func(entry *media.Info, err error) error {
	if err != nil {
		return nil // ошибка отдельного элемента — пропускаем
	}
	fmt.Println(entry.PlaylistIndex, entry.Title)
	return nil
})
```

### Реактивное обновление экстракторов

Реестр потокобезопасен; изменения сразу видны клиенту:

```go
reg := client.Registry()

events, unsubscribe := reg.Subscribe(16) // added / replaced / removed
defer unsubscribe()

updates := make(chan extractor.Update)
go reg.Watch(ctx, updates, nil)

updates <- extractor.Add(myExtractor) // добавить или заменить по имени
updates <- extractor.Remove("vimeo")  // удалить
fmt.Println(<-events)
```

Полный пример — [`example_test.go`](example_test.go).

Обновить экстракторы из сети можно одним вызовом (аналог CLI
`--update-extractors`): `client.UpdateExtractors(ctx, cacheDir)` скачивает и
устанавливает свежий подписанный пакет через HTTP-клиент клиента и возвращает
`true`, если версия изменилась. Текущие версии для экрана «О программе» —
`dlp.Version` (библиотека) и `client.ExtractorsVersion()` (пакет экстракторов).

### Параметры на каждый вызов

Один `*Client` можно использовать для параллельных загрузок с разными колбэками
прогресса, форматами и каталогами — через `Request` и методы `*With` (нулевые поля
наследуются из `Options`):

```go
var wg sync.WaitGroup
for _, u := range urls {
	wg.Add(1)
	go func() {
		defer wg.Done()
		client.DownloadWith(ctx, dlp.Request{
			URL:       u,
			Format:    "bv*+ba/b",
			OutputDir: filepath.Join("out", host(u)),
			Progress:  func(p downloader.Progress) { report(u, p) },
		})
	}()
}
wg.Wait()
```

Есть и `DownloadInfoWith` / `DownloadVideoWith` / `PrepareWith` для уже извлечённой `*media.Info`.

`NoPlaylist` задаётся на конкретный вызов через `Request` + `ExtractWith` /
`DownloadWith` — удобно, когда одна и та же ссылка то «видео в плейлисте», то
плейлист целиком:

```go
one := true
info, _ := client.ExtractWith(ctx, dlp.Request{URL: u, NoPlaylist: &one})
```

Сетевые настройки (cookies, прокси, User-Agent, имитация браузера) меняются
реже, чем на каждый вызов — под них `Clone`: он переиспользует уже
скомпилированные экстракторы и реестр, но даёт собственный HTTP-клиент и
cookie-сессию, поэтому обходится почти бесплатно (в отличие от нового `New`):

```go
// напр. при проверке «вы не робот» повторить запрос с cookies браузера
guest, _ := client.Clone(dlp.CloneOptions{CookiesFromBrowser: "chrome"})
info, err := guest.Extract(ctx, u)
```

### Прямые ссылки на поток (без скачивания)

Для трансляций (Twitch, YouTube Live) и вообще стриминга можно получить адрес
манифеста HLS/DASH с нужными заголовками и отдать его внешнему плееру, ничего
не скачивая:

```go
links, _ := client.StreamLinks(ctx, "https://www.twitch.tv/somechannel")
for _, l := range links {
	fmt.Println(l.URL, l.Protocol, l.IsLive) // master .m3u8 + заголовки в l.Headers
}
```

Для HLS/DASH возвращается адрес манифеста (плеер сам выбирает качество); для
прогрессивных сайтов — прямой URL лучшего формата. CLI: `go-dlp -g URL`.

Для сайтов, отдающих поток только с cookies сессии, `StreamLink.Headers` уже
содержит заголовок `Cookie` из cookie-jar клиента (если его не выставил сам
экстрактор). Тот же заголовок отдельно доступен через `client.CookieHeader(url)`
— например, чтобы передать его mpv (`--http-header-fields`).

Ссылку плеер (mpv, VLC, ffplay, ExoPlayer, AVPlayer, hls.js…) проигрывает сам —
go-dlp при воспроизведении не нужен. Но учитывайте: у YouTube/googlevideo URL
**привязан к IP** извлечения и требует переданного в `Headers` `User-Agent`, а
живёт ~6 часов. Если плеер на том же устройстве (десктопный сценарий) — всё
работает напрямую; для другого IP или плеера без заголовков нужен локальный
прокси (go-dlp ходит на источник с правильными заголовками и ретранслирует на
`127.0.0.1`). Для YouTube HLS-мастер отдаёт до 1080p60; форматы выше (1440p/4K)
есть только как раздельные video+audio (DASH/прогрессив) — их либо скачивают и
склеивают (до 4K, см. ниже), либо играют DASH-плеером.

### Слияние без ffmpeg и свой муксер

Склейка дорожек описана интерфейсом `postprocess.Merger`; реализация выбирается
автоматически (внешний ffmpeg, если найден, иначе встроенный `GoMerger` на чистом Go).
Поведение настраивается:

```go
// Предпочесть встроенный Go-муксер, ffmpeg — только запасной для TS и т.п.:
client, _ := dlp.New(dlp.Options{PreferFFmpeg: new(bool)}) // *bool = false

// Или подставить свою реализацию:
client, _ := dlp.New(dlp.Options{Merger: myMerger}) // myMerger удовлетворяет postprocess.Merger
```

CLI: `--merger auto|ffmpeg|go`. Встроенный Go-муксер склеивает дорожки **в
пределах одного семейства контейнеров** (mp4+m4a → mp4, webm+webm → webm/mkv);
смешанные (напр. видео AV1/MP4 + аудио Opus/WebM) требуют ffmpeg. Поэтому без
кросс-контейнерного муксера выбор формата по умолчанию предпочитает совместимые
пары (`bv*[ext=mp4]+ba[ext=m4a]/bv*[ext=webm]+ba[ext=webm]/…`), так что YouTube
качается до 4K и без ffmpeg.

### Типы ошибок

Ошибки экстракции — категории для `errors.Is`: `extractor.ErrUnsupportedURL`,
`ErrLoginRequired`, `ErrGeoRestricted`, `ErrBotCheck` (проверка «вы не робот»),
`ErrPlayerUnsupported` (изменился плеер сайта — экстрактор пора обновить),
`ErrUnavailable`, `ErrNoFormats`:

```go
_, err := client.Download(ctx, url)
switch {
case errors.Is(err, extractor.ErrBotCheck):
	// нужны cookies браузера или PO Token
case errors.Is(err, extractor.ErrLoginRequired):
	// приватное/с возрастным ограничением — передайте --cookies
}
```

## Поддерживаемые сайты

| Сайт | Экстракторы |
|---|---|
| YouTube | `youtube`, `youtube:tab` (плейлисты, каналы), `youtube:search` (`ytsearchN:`) |
| Twitch | `twitch:vod`, `twitch:clips`, `twitch:stream`, `twitch:videos` |
| Kick | `kick:live`, `kick:vod`, `kick:clip`, `kick:videos` |
| VK | `vk` (видео, клипы, embed), `vk:uservideos` (видео сообщества/пользователя, плейлисты) |
| Mail.ru | `mailru` (JS) |
| Smotrim (ВГТРК) | `smotrim` (JS; видео, аудио, каналы, бренды) |
| Первый канал | `firsttv` (JS) |
| RT | `rt` (JS) |
| Одноклассники | `odnoklassniki` |
| Rutube | `rutube`, `rutube:playlist` |
| SoundCloud | `soundcloud`, `soundcloud:set`, `soundcloud:user` |
| Bandcamp | `bandcamp`, `bandcamp:album` |
| Vimeo | `vimeo` (без cookies многие видео недоступны) |
| Dailymotion | `dailymotion` |
| Reddit | `reddit` (нужен `--cookies` залогиненного аккаунта) |
| Bluesky | `bluesky` |
| X (Twitter) | `twitter` (JS) |
| TikTok | `tiktok` (JS) |
| Odysee / LBRY | `odysee` (JS) |
| TED | `ted` (JS) |
| Loom | `loom` (JS; HLS-only ролики пока без скачивания) |
| Telegram | `telegram:embed` (публичные каналы) |
| Coub | `coub` |
| Streamable | `streamable` |
| Internet Archive | `archive.org` |
| PeerTube | `peertube` (любой инстанс) |
| 18+ | `xvideos`, `xnxx`, `pornhub`, `redtube`, `youporn`, `xhamster`, `eporner` |
| Embed-плееры | `brightcove`, `kaltura`, `jwplayer`, `wistia` (JS; передавайте URL самого embed/плеера) |
| Остальное | `generic`: прямые ссылки, m3u8, mpd, `<video>`, og:video, JSON-LD, iframe- и inline-встраивания (Brightcove/Wistia/JWPlayer/Kaltura) |

Экстракторы с пометкой «JS» написаны на JavaScript и обновляются без нового релиза
(`--update-extractors`, `Client.Scripts()`), см. [docs/script-extractors.md](docs/script-extractors.md).

## Структура проекта

| Пакет | Назначение | Аналог в yt-dlp |
|---|---|---|
| `dlp` (корень) | `Client`: экстракция → выбор форматов → загрузка → слияние | `YoutubeDL.py` |
| `media` | `Info`, `Format`, `Subtitle`, `Fragment` | info dict |
| `extractor` | интерфейс `Extractor`, `Registry`, `Env` (HTTP), хелперы разбора | `extractor/common.py`, `utils` |
| `extractor/extractortest` | хелперы тестов экстракторов | `_TESTS` |
| `extractors/*` | экстракторы сайтов; `extractors/all` — набор по умолчанию | `extractor/*.py` |
| `script` | JS-экстракторы (goja), подписанные пакеты и их обновление на лету | — |
| `cmd/go-dlp-pack` | сборка и подпись пакета JS-экстракторов | — |
| `format` | сортировка форматов и язык выбора (`bv*+ba/b`, `[height<=720]`, `/`, `+`, `,`) | `build_format_selector` |
| `downloader` | HTTP (докачка, Range-чанки), HLS (AES-128, live), DASH-фрагменты (live) | `downloader/` |
| `hls`, `dash` | парсеры m3u8 и MPD | `_parse_m3u8_formats`, `_parse_mpd_formats` |
| `impersonate` | имитация браузера (uTLS + HTTP/2-отпечаток) | `networking/_curlcffi.py` |
| `postprocess` | слияние/ремукс дорожек: `Merger` (ffmpeg или `GoMerger` на чистом Go — MP4/WebM/MKV) | `postprocessor/` |
| `outtmpl` | шаблоны имён файлов | `prepare_outtmpl` |
| `cmd/go-dlp`, `internal/cli` | CLI на cobra | `options.py` |

## Как добавить свой экстрактор

Новые сайты лучше писать на JavaScript (`script/scripts/*.js`): такие экстракторы
доходят до пользователей без релиза приложения, см.
[docs/script-extractors.md](docs/script-extractors.md). На Go стоит писать то, что
требует тяжёлой логики или производительности (например, расшифровку плеера YouTube).

### 1. Реализуйте интерфейс

Экстрактор — любой тип с тремя методами (`extractor.Extractor`):

```go
type Extractor interface {
	Name() string                // уникальный ключ, напр. "example" или "example:playlist"
	Suitable(rawURL string) bool // подходит ли URL
	Extract(ctx context.Context, env *Env, rawURL string) (*media.Info, error)
}
```

Необязательно: `Priority() int` (больше — проверяется раньше; generic — `extractor.PriorityFallback`)
и `Description() string` (для `go-dlp extractors`).

Минимальный пример — `extractors/example/example.go`:

```go
// Package example — экстрактор example.com.
package example

import (
	"context"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/hls"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "example"

// Аналог _VALID_URL: именованные группы доступны через Groups.
var matcher = ex.MustMatcher(`^https?://(?:www\.)?example\.com/videos/(?P<id>\d+)`)

type Example struct{}

func New() *Example { return &Example{} }

func (*Example) Name() string           { return ieKey }
func (*Example) Description() string    { return "Example videos" }
func (*Example) Suitable(u string) bool { return matcher.Match(u) }

func (*Example) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	id := g["id"]

	// env — HTTP-клиент с cookie-jar, прокси, User-Agent и имитацией браузера.
	var data any
	if err := env.GetJSON(ctx, "https://api.example.com/videos/"+id, nil, &data); err != nil {
		return nil, ex.Wrap(ieKey, id, err, "unable to download video JSON")
	}
	if ex.Bool(data, "private") {
		return nil, ex.Errorf(ieKey, id, ex.ErrLoginRequired, "private video")
	}

	info := &media.Info{
		Kind:     media.KindVideo,
		ID:       id,
		Title:    ex.Str(data, "title"), // безопасный обход JSON: Str/Int/Float/Map/Slice
		Uploader: ex.Str(data, "owner", "name"),
		Duration: ex.Float(data, "duration"),
	}
	for _, f := range ex.Slice(data, "files") {
		info.Formats = append(info.Formats, &media.Format{
			FormatID: ex.Str(f, "quality"),
			URL:      ex.Str(f, "url"),
			Ext:      "mp4",
			Height:   int(ex.Int(f, "height")),
		})
	}
	if u := ex.Str(data, "hls"); u != "" {
		if body, err := env.GetText(ctx, u, nil); err == nil {
			info.Formats = append(info.Formats, hls.ParseMaster(body, u, "hls")...)
		}
	}
	if len(info.Formats) == 0 {
		return nil, ex.Errorf(ieKey, id, ex.ErrNoFormats, "no formats found")
	}
	return info, nil
}
```

Что ещё может вернуть экстрактор:

- **плейлист** — `Kind: media.KindPlaylist` с `Entries`; элементы можно отдавать ссылками
  `media.NewURL(url, ieKey)`, клиент извлечёт их сам (при постраничной загрузке учитывайте `env.MaxEntries`);
- **ссылку на другой экстрактор** — `media.NewURL(url, "")` (например, встроенный YouTube-плеер);
- **отказ от URL** — `ex.ErrUnsupportedURL` из `Extract`: клиент передаст URL следующему подходящему
  экстрактору (так PeerTube отсеивает хосты, которые не являются PeerTube).

Полезное в пакете `extractor`: `Env.Fetch/GetText/GetJSON/PostJSON`, `Request{Impersonate, NoCookies, Query, JSON}`,
`Env.Arg` (аргументы `--extractor-args`), `Env.Cookie`, `JSONAfter` (JSON из `<script>`), `MetaContent`,
`JSONLD`, `FindTags`, `ParseDuration`, `UnifiedDate`, `URLJoin`. Парсеры потоков — `hls.ParseMaster`,
`dash.Parse`. Категории ошибок: `ErrUnavailable`, `ErrLoginRequired`, `ErrGeoRestricted`, `ErrNoFormats`.

### 2. Зарегистрируйте

Добавьте конструктор в `extractors/all/all.go` — экстрактор попадёт в набор по умолчанию (CLI и
`dlp.New` без своего реестра). Порядок проверки задаёт `Priority()`, при равенстве — порядок регистрации.

В своём приложении менять `all.go` не нужно — достаточно добавить экстрактор в реестр клиента:

```go
client.Registry().Register(example.New())
```

### 3. Напишите тесты

- **Маршрутизация URL** — добавьте случаи в `extractors/all/match_test.go`: проверяется, что URL достаётся
  именно вашему экстрактору и не перехватывается соседними.
- **Онлайн-тест** — в `extractors/all/online_test.go` через `extractortest` (аналог `_TESTS` yt-dlp):

```go
func TestOnlineExample(t *testing.T) {
	et.Online(t) // запускается только при GODLP_ONLINE=1
	et.Extract(t, example.New(), "https://example.com/videos/123", et.Expect{
		ID: "123", Title: "re:^Example", MinFormats: 1, MinDuration: 10,
	})
}
```

- **Чистая логика** (расшифровки, хеши, парсеры) — обычные юнит-тесты в пакете экстрактора; эталонные
  значения удобно генерировать кодом yt-dlp.

```sh
go test ./...                                            # офлайн-тесты
GODLP_ONLINE=1 go test ./extractors/all -run Online -v   # реальные запросы к сайтам
```

### Правила

- Сверяйтесь с соответствующим экстрактором yt-dlp — там описаны эндпоинты, поля и известные ловушки.
- Не обходите DRM, капчи, антибот-проверки и обязательную проверку возраста: в таких случаях возвращайте
  понятную ошибку (`ErrLoginRequired` с подсказкой про `--cookies`).
- Перед PR запустите `gofmt`, `go vet ./...` и `go test -race ./...`.

## Ограничения

- YouTube: по умолчанию клиент без JS-плеера (`visionos`) с готовыми URL. Проверено вживую
  (2026-11): `visionos`, `tv_simply` и `android_vr` отдают URL-форматы **до 2160p (4K) без PO Token**,
  поэтому опасение «без SABR пропадут форматы выше 1080p» сейчас не актуально. Расшифровка подписи и
  n-параметра через JS (goja) реализована и для «классических», и для новейших es6-плееров
  (весь код плеера исполняется в goja, функция-подписыватель URL находится структурно). Клиенты
  с шифрованными форматами включаются через `--extractor-args youtube:player_client=web` (или `tv`).
  Клиент `web` работает в гостевом режиме (без visitor data), но YouTube сейчас отдаёт ему
  почти все форматы только через SABR (`serverAbrStreamingUrl`, без `url`/`signatureCipher`);
  SABR не поддерживается, такие форматы пропускаются с предупреждением (а если форматов не
  осталось вовсе — возвращается понятная ошибка; при неразбираемом base.js — `ErrPlayerUnsupported`).
  PO Token нельзя сгенерировать в goja (BotGuard требует браузера), но его можно передать извне
  (от браузера/провайдера) для URL-клиентов, требующих `pot`:
  `--extractor-args "youtube:player_client=web;po_token=web.gvs+XXXX,web.player+YYYY;visitor_data=…"`
  (контексты `gvs`/`player`/`subs`; ключ без клиента применяется ко всем; `visitor_data` должен
  совпадать с тем, с которым сгенерирован токен). Но вживую `web` сейчас отдаёт только SABR, и его
  `pot` уходит внутрь тела SABR-запроса, а не в URL, — так что подстановкой `pot` в URL клиент `web`
  не разблокировать: для него нужен полный SABR. Клиент `tv` возвращает UNPLAYABLE, поэтому `web`/`tv`
  не включены по умолчанию. SABR к загрузке пока не подключён; в пакете `extractors/youtube/sabr`
  есть проверяемый каркас (кодек UMP и protobuf-wire), но по прежним замерам поток без валидного
  attestation-токена встаёт на ~60 с (`STREAM_PROTECTION_STATUS → ATTESTATION_REQUIRED`).
  Если `visionos` не дал форматов (например, «Sign in to confirm you're not a bot»),
  автоматически пробуется `tv_simply`: подписи и n расшифровываются в goja, PO Token не нужен.
  При cookies аккаунта запросы клиентов `web`/`tv` подписываются `SAPISIDHASH`.
- DASH: статические манифесты с несколькими Period склеиваются (сегменты одинаковых представлений);
  живые (dynamic) поддерживают SegmentTemplate и SegmentList у live-края; живой SegmentBase и DRM не поддерживаются.
- Учётные данные: `-u/--username`, `-p/--password`, `--twofactor`, а также `--netrc`/`--netrc-location`
  (формат `.netrc`) доступны экстракторам через `env.Credentials(machine)`; собственно процедуры входа
  зависят от сайта. Универсальный способ авторизации — `--cookies` (Netscape `cookies.txt` или JSON-экспорт
  Cookie-Editor/EditThisCookie) либо `--cookies-from-browser BROWSER[:PROFILE]` (Firefox, Chromium-браузеры
  с расшифровкой средствами ОС, Safari на macOS).
- JS-экстракторы из сетевых обновлений исполняются в песочнице: нет файловой системы и процессов,
  `cookie(url, name)` доступен только в пределах регистрируемого домена (eTLD+1) извлекаемого URL,
  и на сторонние хосты cookies не подмешиваются — скрипт не может прочитать и переслать cookies
  чужого домена (и не может сам расширить себе allowlist через `hosts`/`match`). Встроенные и
  локальные (`--extractor-scripts`) скрипты доверенные.
- Не реализованы: Mixcloud (поток намеренно зашифрован сайтом против скачивания), Rumble и SpankBang
  (Cloudflare блокирует запросы).

## Благодарности и лицензия

Проект основан на идеях, структуре и знаниях о сайтах из [yt-dlp](https://github.com/yt-dlp/yt-dlp)
(и его предшественника youtube-dl) — огромное спасибо его авторам и сообществу.

Как и yt-dlp, go-dlp распространяется под лицензией [Unlicense](LICENSE) — это общественное достояние
(public domain): код можно свободно копировать, изменять и использовать в любых целях.
