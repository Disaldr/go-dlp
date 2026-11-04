# go-dlp

Загрузчик видео и аудио на Go — библиотека и CLI. Порт ключевой функциональности
[yt-dlp](https://github.com/yt-dlp/yt-dlp): экстракторы сайтов, язык выбора форматов,
HLS/DASH, слияние дорожек через ffmpeg, шаблоны имён файлов.

- **Библиотека в первую очередь**: всё, что умеет CLI, доступно из Go-кода.
- **Реактивный реестр экстракторов**: экстракторы можно добавлять, заменять и удалять на лету через канал.
- **35 сайтов**, плюс generic-экстрактор для прямых ссылок, m3u8/mpd и встраиваний.
- **Имитация браузера**: TLS- и HTTP/2-отпечаток Chrome/Firefox/Safari (`--impersonate`).

## Установка

```sh
go install github.com/Disaldr/go-dlp/cmd/go-dlp@latest
# или из исходников
git clone https://github.com/Disaldr/go-dlp && cd go-dlp && go build -o bin/go-dlp ./cmd/go-dlp
```

Требуется Go 1.25+. Для слияния видео и аудио и перепаковки HLS нужен [ffmpeg](https://ffmpeg.org/) в `PATH`
(или `--ffmpeg-location`).

## CLI

```sh
go-dlp -F https://youtu.be/jNQXAC9IVRw               # список форматов
go-dlp https://youtu.be/jNQXAC9IVRw                  # скачать лучшее (видео+аудио со слиянием)
go-dlp -f "bv*[height<=720]+ba/b" URL                # выбор форматов, как в yt-dlp
go-dlp -o "%(uploader)s/%(title)s.%(ext)s" URL       # шаблон имени файла
go-dlp -j URL | jq .title                            # JSON без загрузки
go-dlp --playlist-end 5 -e "ytsearch10:golang"       # поиск, первые 5 названий
go-dlp --cookies cookies.txt URL                     # cookies: Netscape cookies.txt или JSON-экспорт
go-dlp --impersonate chrome URL                      # имитация браузера
go-dlp extractors                                    # список экстракторов
go-dlp --help                                        # все флаги
```

Прямые эфиры (HLS/DASH) записываются с live-края; `Ctrl+C` останавливает запись, скачанное сохраняется
и сливается. Повторный `Ctrl+C` — немедленный выход.

## Использование как библиотеки

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

## Поддерживаемые сайты

| Сайт | Экстракторы |
|---|---|
| YouTube | `youtube`, `youtube:tab` (плейлисты, каналы), `youtube:search` (`ytsearchN:`) |
| Twitch | `twitch:vod`, `twitch:clips`, `twitch:stream`, `twitch:videos` |
| Kick | `kick:live`, `kick:vod`, `kick:clip`, `kick:videos` |
| VK | `vk` (видео, клипы, embed), `vk:uservideos` (видео сообщества/пользователя, плейлисты) |
| Одноклассники | `odnoklassniki` |
| Rutube | `rutube`, `rutube:playlist` |
| SoundCloud | `soundcloud`, `soundcloud:set`, `soundcloud:user` |
| Bandcamp | `bandcamp`, `bandcamp:album` |
| Vimeo | `vimeo` (без cookies многие видео недоступны) |
| Dailymotion | `dailymotion` |
| Reddit | `reddit` (нужен `--cookies` залогиненного аккаунта) |
| Bluesky | `bluesky` |
| Telegram | `telegram:embed` (публичные каналы) |
| Coub | `coub` |
| Streamable | `streamable` |
| Internet Archive | `archive.org` |
| PeerTube | `peertube` (любой инстанс) |
| 18+ | `xvideos`, `xnxx`, `pornhub`, `redtube`, `youporn`, `xhamster`, `eporner` |
| Остальное | `generic`: прямые ссылки, m3u8, mpd, `<video>`, og:video, JSON-LD, iframe-встраивания |

## Структура проекта

| Пакет | Назначение | Аналог в yt-dlp |
|---|---|---|
| `dlp` (корень) | `Client`: экстракция → выбор форматов → загрузка → слияние | `YoutubeDL.py` |
| `media` | `Info`, `Format`, `Subtitle`, `Fragment` | info dict |
| `extractor` | интерфейс `Extractor`, `Registry`, `Env` (HTTP), хелперы разбора | `extractor/common.py`, `utils` |
| `extractor/extractortest` | хелперы тестов экстракторов | `_TESTS` |
| `extractors/*` | экстракторы сайтов; `extractors/all` — набор по умолчанию | `extractor/*.py` |
| `format` | сортировка форматов и язык выбора (`bv*+ba/b`, `[height<=720]`, `/`, `+`, `,`) | `build_format_selector` |
| `downloader` | HTTP (докачка, Range-чанки), HLS (AES-128, live), DASH-фрагменты (live) | `downloader/` |
| `hls`, `dash` | парсеры m3u8 и MPD | `_parse_m3u8_formats`, `_parse_mpd_formats` |
| `impersonate` | имитация браузера (uTLS + HTTP/2-отпечаток) | `networking/_curlcffi.py` |
| `postprocess` | ffmpeg: слияние, ремукс | `postprocessor/` |
| `outtmpl` | шаблоны имён файлов | `prepare_outtmpl` |
| `cmd/go-dlp`, `internal/cli` | CLI на cobra | `options.py` |

## Как добавить свой экстрактор

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

- YouTube: по умолчанию клиент без JS-плеера (`visionos`) с готовыми URL. Расшифровка подписи и
  n-параметра через JS (goja) реализована и для «классических», и для новейших es6-плееров
  (весь код плеера исполняется в goja, функция-подписыватель URL находится структурно). Клиенты
  с шифрованными форматами включаются через `--extractor-args youtube:player_client=web` (или `tv`).
  Клиент `web` работает в гостевом режиме (без visitor data) — YouTube выдаёт playback-грант без
  PO Token; надёжность зависит от видео. PO Token как таковой не поддерживается, клиент `tv`
  возвращает UNPLAYABLE, поэтому `web`/`tv` не включены по умолчанию.
- DASH: в статических манифестах берётся первый Period, живые — только SegmentTemplate; DRM не поддерживается.
- Логин по паролю не поддерживается — используйте `--cookies` (Netscape `cookies.txt` или JSON-экспорт
  Cookie-Editor/EditThisCookie) либо `--cookies-from-browser BROWSER[:PROFILE]` (Firefox, Chromium-браузеры
  с расшифровкой средствами ОС, Safari на macOS).
- Не реализованы: Mixcloud (поток намеренно зашифрован сайтом против скачивания), Rumble и SpankBang
  (Cloudflare блокирует запросы).

## Благодарности и лицензия

Проект основан на идеях, структуре и знаниях о сайтах из [yt-dlp](https://github.com/yt-dlp/yt-dlp)
(и его предшественника youtube-dl) — огромное спасибо его авторам и сообществу.

Как и yt-dlp, go-dlp распространяется под лицензией [Unlicense](LICENSE) — это общественное достояние
(public domain): код можно свободно копировать, изменять и использовать в любых целях.
