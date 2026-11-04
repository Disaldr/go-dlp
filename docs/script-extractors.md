# JS-экстракторы и обновление без релиза

Сайты меняются чаще, чем выходят версии приложений. Поэтому экстракторы можно
писать на JavaScript: они исполняются встроенным движком [goja](https://github.com/dop251/goja)
и доставляются **подписанным пакетом**, который приложение скачивает само. Новый
сайт или исправление попадает к пользователям без обновления go-dlp или
приложения, которое его встраивает.

```
script/scripts/*.js + VERSION ──go-dlp-pack──▶ pack.json + pack.json.sig ──▶ GitHub Release «extractors»
                                                                                  │
приложение: встроенные скрипты (вшиты при сборке) ◀── Updater (ETag, ed25519, кеш) ┘
            └─ Manager ─▶ Registry: скрипт подменяет одноимённый Go-экстрактор, Go-версия — запасная
```

## Как это работает

- **Встроенный набор.** Каталог `script/scripts` вшит в бинарь (`go:embed`), поэтому
  JS-экстракторы работают и без сети. Его версия — файл `VERSION`.
- **Пакет.** `pack.json` содержит все скрипты, версию и требуемую версию API хоста
  (`host_api`). `pack.json.sig` — подпись ed25519 над точными байтами `pack.json`.
  Открытый ключ вшит в `script.DefaultPublicKey`.
- **Установка.** Пакет ставится, только если подпись верна, `host_api` не больше
  `script.HostAPI` и версия больше установленной. Откат на старую версию невозможен.
- **Замена Go-экстракторов.** Скрипт с именем существующего экстрактора (например,
  `youtube`) подменяет его в реестре. Встроенный остаётся запасным: если скрипт упал
  с ошибкой, которая не является категорией (`fail.*`), вызывается Go-версия. Если в
  следующем пакете скрипта нет, встроенный возвращается.
- **Кеш.** Последний проверенный пакет лежит на диске. При запуске он поднимается без
  сети, и его подпись проверяется заново.
- **Песочница.** У скрипта нет доступа к файлам и процессам. Сеть идёт только через
  `Env` (cookies, прокси, имитация браузера), есть таймауты (`ExtractTimeout` 2 мин,
  загрузка скрипта 5 с) и прерывание по `ctx`.

## Встраивание (GliGO и др.)

```go
client, _ := dlp.New(dlp.Options{})          // встроенные JS-экстракторы уже установлены
u := &script.Updater{CacheDir: filepath.Join(cacheDir, "go-dlp-extractors")}
go u.Run(ctx, client.Scripts(), 6*time.Hour) // кеш → проверка сразу → затем каждые 6 ч
```

Свой хостинг: `Updater{URL: "https://example.com/pack.json", PublicKey: myKey}`. Подпись
ищется по адресу `URL + ".sig"`.

CLI: `--update-extractors` проверяет обновление (кеш в `os.UserCacheDir()/go-dlp/extractors`
поднимается всегда), `--extractor-pack-url` задаёт свой URL, а `--extractor-scripts DIR`
подключает локальный каталог без подписи (для разработки).

## Написание скрипта

```js
// script/scripts/example.js
extractor({
  name: "example",                       // ie_key; совпадение с Go-экстрактором = подмена
  description: "Example videos",
  match: [String.raw`^https?://(?:www\.)?example\.com/v/(\d+)`],  // синтаксис Go RE2
  extract(url, m) {                      // m — подгруппы совпавшего выражения
    const page = http.get(url).text;
    const data = jsonAfter(page, "window.__DATA__ =");
    if (!data) fail.unavailable("video not found", m[1]);
    return {
      id: m[1],
      title: data.title,
      formats: [{ url: data.mp4, height: 720 }, ...hls(data.m3u8)],
    };
  },
});
```

Результат — объект с полями `media.Info` в именах JSON yt-dlp: `id`, `title`,
`formats[]` (`url`, `format_id`, `ext`, `width`, `height`, `tbr`, `vcodec`, `acodec`,
`http_headers`…), `_type: "playlist"` + `entries[]`, `thumbnail`, `duration`,
`timestamp`, `uploader`… Протокол и расширение выводятся сами, если их не указать.

### API хоста (HostAPI 1)

| Функция | Что делает |
|---|---|
| `http.get(url, opts)`, `http.post(url, opts)`, `http.request({url, method, ...})` | `opts`: `headers`, `query`, `json`, `body`, `form`, `impersonate`, `noCookies`, `allowErrors`. Возвращает `{status, url, headers, text, json()}`. Статус ≥ 400 бросает исключение, если не задан `allowErrors` |
| `hls(url, {id, headers})`, `dash(url, {id})` | форматы из m3u8 / MPD |
| `urlResult(url, ieKey)` | передать URL другому экстрактору |
| `jsonAfter(text, ...markers)` | JSON сразу после маркера (или `null`) |
| `urlJoin`, `unescapeHTML`, `parseDuration` | утилиты |
| `cookie(url, name)`, `arg(key, default)` | cookies и `--extractor-args NAME:key=value` |
| `base64.*`, `hash.md5/sha1/sha256` | кодирование и хеши (hex) |
| `log.debug`, `log.warn` | журнал |
| `fail.unsupported/unavailable/login/geo/noFormats(msg, id)` | бросить ошибку-категорию |

Если скрипту нужно то, чего в API нет, расширьте `script/host.go` и увеличьте
`script.HostAPI`. Пакеты со скриптами, требующими нового API, старые приложения
не установят: они останутся на последнем совместимом пакете.

### Разработка и проверка

```sh
go run ./cmd/go-dlp --extractor-scripts script/scripts -j URL   # прогон без пересборки
go test ./script                                                  # компиляция и совпадение URL
```

## Публикация пакета

1. Увеличьте число в `script/scripts/VERSION`.
2. `go run ./cmd/go-dlp-pack`. Подпись делается ключом `~/.config/go-dlp/pack-signing.key`;
   утилита проверяет, что ключ соответствует `script.DefaultPublicKey`.
3. `gh release upload extractors dist/pack.json dist/pack.json.sig --clobber`.

Закрытый ключ не хранится в репозитории. Если он потерян, сгенерируйте новый и
замените `DefaultPublicKey`: тогда новые пакеты примут только версии приложения с новым ключом.
