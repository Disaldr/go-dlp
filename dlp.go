// Package dlp — библиотека загрузки видео, порт ключевой функциональности
// yt-dlp на Go.
//
// Минимальный пример:
//
//	c, _ := dlp.New(dlp.Options{})
//	results, err := c.Download(ctx, "https://youtu.be/dQw4w9WgXcQ")
//
// Экстракторы живут в реестре (extractor.Registry); его можно менять на лету —
// напрямую или через канал обновлений (Registry().Watch).
package dlp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Disaldr/go-dlp/downloader"
	"github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/extractors/all"
	"github.com/Disaldr/go-dlp/format"
	"github.com/Disaldr/go-dlp/impersonate"
	"github.com/Disaldr/go-dlp/media"
	"github.com/Disaldr/go-dlp/outtmpl"
	"github.com/Disaldr/go-dlp/postprocess"
	"github.com/Disaldr/go-dlp/script"
)

// Version — версия библиотеки.
const Version = "0.1.0"

// Options — настройки клиента. Нулевое значение пригодно к использованию.
type Options struct {
	// Format — выражение выбора форматов; по умолчанию "bv*+ba/b" (с ffmpeg) или "b/bv*+ba".
	Format string
	// OutputTemplate — шаблон имени файла (outtmpl.Default по умолчанию).
	OutputTemplate string
	// OutputDir — каталог загрузки.
	OutputDir string
	// RestrictFilenames — только ASCII и без пробелов.
	RestrictFilenames bool
	// MergeOutputFormat — контейнер для слияния (mp4, mkv, webm...).
	MergeOutputFormat string
	// Overwrite — перезаписывать существующие файлы.
	Overwrite bool

	// Registry — реестр экстракторов (по умолчанию встроенные).
	Registry *extractor.Registry
	// NoBuiltinScripts — не устанавливать встроенные JS-экстракторы
	// (пакет script). Обновления из сети ставятся через Client.Scripts().
	NoBuiltinScripts bool
	// ExtractorScriptsDir — каталог с *.js (и необязательным VERSION),
	// загружаемый без подписи поверх встроенных — для разработки скриптов.
	ExtractorScriptsDir string
	// ExtractorArgs — аргументы экстракторов: ie -> ключ -> значения.
	ExtractorArgs map[string]map[string][]string

	HTTPClient *http.Client
	Proxy      string
	UserAgent  string
	// CookiesFile — файл cookies: Netscape cookies.txt (как --cookies в yt-dlp)
	// или JSON-экспорт расширения браузера (Cookie-Editor, EditThisCookie).
	CookiesFile string
	// CookiesFromBrowser — читать cookies из BROWSER[:PROFILE], например
	// "chrome", "chrome:Profile 1", "firefox:/path/to/profile" или "safari".
	// При совместном использовании CookiesFile имеет приоритет.
	CookiesFromBrowser string
	// Impersonate — имитировать браузер во всех запросах ("any", "chrome", "safari-16.0"...).
	// Экстракторы могут включать имитацию и для отдельных запросов сами.
	Impersonate string

	// FFmpegLocation — путь к ffmpeg или каталогу с ним.
	FFmpegLocation string
	// Merger — реализация склейки/ремукса дорожек. Если nil, выбирается
	// автоматически: внешний ffmpeg (если найден), иначе встроенная склейка
	// на чистом Go (postprocess.GoMerger). Позволяет подставить свою.
	Merger postprocess.Merger
	// PreferFFmpeg — при наличии и ffmpeg, и встроенного муксера
	// предпочитать ffmpeg (по умолчанию true). Если false и Merger не задан,
	// используется встроенный Go-муксер, а ffmpeg — только как запасной для
	// неподдерживаемых им контейнеров (например, ремукс MPEG-TS).
	PreferFFmpeg *bool

	// NoPlaylist — для ссылок «видео + плейлист» брать только видео.
	NoPlaylist bool
	// PlaylistStart/PlaylistEnd — диапазон элементов (с 1, включительно; 0 — без ограничения).
	PlaylistStart, PlaylistEnd int

	// Retries, Fragments, ChunkSize, RateLimit — настройки загрузчика.
	Retries   int
	Fragments int
	ChunkSize int64
	RateLimit int64

	// WriteSubtitles — сохранять субтитры (языки SubtitleLangs, по умолчанию все).
	WriteSubtitles bool
	SubtitleLangs  []string

	// Progress — колбэк прогресса загрузки.
	Progress downloader.ProgressFunc
	// Logger — логгер (по умолчанию — без вывода).
	Logger *slog.Logger
}

// Request — параметры одной загрузки, переопределяющие настройки клиента на
// конкретный вызов (методы *With). Нулевые поля наследуют значения из Options:
// пустая строка и nil-указатель означают «взять из клиента». Это позволяет
// одному *Client качать несколько файлов параллельно с разными колбэками
// прогресса, форматами и каталогами.
type Request struct {
	// URL — адрес для извлечения (используется DownloadWith).
	URL string
	// Format — выражение выбора форматов на этот вызов.
	Format string
	// OutputDir — каталог загрузки на этот вызов.
	OutputDir string
	// OutputTemplate — шаблон имени файла на этот вызов.
	OutputTemplate string
	// MergeOutputFormat — контейнер для слияния на этот вызов.
	MergeOutputFormat string
	// Overwrite — перезаписывать существующие файлы (nil — наследовать).
	Overwrite *bool
	// RestrictFilenames — только ASCII без пробелов (nil — наследовать).
	RestrictFilenames *bool
	// WriteSubtitles — сохранять субтитры (nil — наследовать).
	WriteSubtitles *bool
	// SubtitleLangs — языки субтитров (nil — наследовать).
	SubtitleLangs []string
	// Progress — колбэк прогресса именно этой загрузки.
	Progress downloader.ProgressFunc
}

// callOpts — эффективные настройки одной загрузки (Options ⊕ Request).
type callOpts struct {
	format            string
	outputDir         string
	outputTemplate    string
	mergeOutputFormat string
	restrictFilenames bool
	overwrite         bool
	writeSubtitles    bool
	subtitleLangs     []string
	progress          downloader.ProgressFunc
}

// Client — основной объект библиотеки (аналог YoutubeDL).
type Client struct {
	opts    Options
	reg     *extractor.Registry
	scripts *script.Manager
	env     *extractor.Env
	ffmpeg  *postprocess.FFmpeg
	merger  postprocess.Merger
	log     *slog.Logger
}

// New создаёт клиента.
func New(opts Options) (*Client, error) {
	c := &Client{opts: opts, reg: opts.Registry, log: opts.Logger}
	if c.reg == nil {
		c.reg = all.Registry()
	}
	c.scripts = script.NewManager(c.reg)
	if !opts.NoBuiltinScripts {
		p, err := script.Builtin()
		if err != nil {
			return nil, fmt.Errorf("builtin scripts: %w", err)
		}
		if _, err := c.scripts.Install(p); err != nil {
			return nil, err
		}
	}
	if opts.ExtractorScriptsDir != "" {
		p, err := script.LoadDir(os.DirFS(opts.ExtractorScriptsDir))
		if err != nil {
			return nil, fmt.Errorf("extractor scripts: %w", err)
		}
		// Локальные скрипты ставятся всегда, даже с меньшей VERSION.
		p.Version = max(p.Version, c.scripts.Version()+1)
		if _, err := c.scripts.Install(p); err != nil {
			return nil, err
		}
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	hc := opts.HTTPClient
	if hc == nil {
		var proxy *url.URL
		if opts.Proxy != "" {
			p, err := url.Parse(opts.Proxy)
			if err != nil {
				return nil, fmt.Errorf("invalid proxy: %w", err)
			}
			proxy = p
		}
		hc = extractor.DefaultHTTPClient(proxy)
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = extractor.DefaultUserAgent
	}
	args := map[string]map[string][]string{}
	for ie, kv := range opts.ExtractorArgs {
		args[strings.ToLower(ie)] = kv
	}
	// Если пользователь берёт cookies из браузера, но не задал имитацию и
	// User-Agent, по умолчанию имитируем тот же браузер. Защитные cookies
	// (cf_clearance Cloudflare, datadome и т.п.) привязаны к TLS-отпечатку и
	// User-Agent браузера, который их выдал; без совпадения сервер их не
	// примет и снова покажет проверку. Это не обход проверки, а продолжение
	// уже пройденной пользователем в его же браузере сессии.
	imp := opts.Impersonate
	if imp == "" && opts.CookiesFromBrowser != "" && opts.UserAgent == "" {
		if t := browserImpersonate(opts.CookiesFromBrowser); t != "" {
			imp = t
			c.log.Debug("matching TLS/User-Agent to browser for its cookies", "impersonate", imp)
		}
	}
	if imp != "" {
		if _, err := impersonate.Lookup(imp); err != nil {
			return nil, err
		}
	}
	var proxyFn impersonate.ProxyFunc
	if opts.Proxy != "" {
		p, _ := url.Parse(opts.Proxy)
		proxyFn = http.ProxyURL(p)
	}
	c.env = &extractor.Env{
		HTTP: hc, UserAgent: ua, Logger: c.log, Registry: c.reg,
		Args: args, NoPlaylist: opts.NoPlaylist, MaxEntries: opts.PlaylistEnd,
		Impersonate:  imp,
		Impersonator: &impersonate.Pool{Jar: hc.Jar, Proxy: proxyFn},
	}
	if opts.CookiesFromBrowser != "" {
		n, err := extractor.LoadCookiesFromBrowser(hc, opts.CookiesFromBrowser)
		if err != nil {
			return nil, fmt.Errorf("load browser cookies: %w", err)
		}
		c.log.Debug("browser cookies loaded", "count", n)
	}
	if opts.CookiesFile != "" {
		n, err := extractor.LoadCookiesFile(hc, opts.CookiesFile)
		if err != nil {
			return nil, fmt.Errorf("load cookies: %w", err)
		}
		c.log.Debug("cookies loaded", "count", n, "file", opts.CookiesFile)
	}
	if ff, err := postprocess.FindFFmpeg(opts.FFmpegLocation); err == nil {
		c.ffmpeg = ff
	} else if opts.FFmpegLocation != "" {
		return nil, err
	}
	c.merger = c.pickMerger()
	if opts.Format != "" {
		if _, err := format.Compile(opts.Format); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Registry возвращает реестр экстракторов клиента.
func (c *Client) Registry() *extractor.Registry { return c.reg }

// Scripts возвращает менеджер JS-экстракторов. Обновление без перевыпуска
// приложения:
//
//	u := &script.Updater{CacheDir: dir}
//	go u.Run(ctx, client.Scripts(), 6*time.Hour)
func (c *Client) Scripts() *script.Manager { return c.scripts }

// Env возвращает окружение экстракторов.
func (c *Client) Env() *extractor.Env { return c.env }

// HasFFmpeg сообщает, найден ли ffmpeg.
func (c *Client) HasFFmpeg() bool { return c.ffmpeg != nil }

// Merger возвращает активную реализацию склейки дорожек.
func (c *Client) Merger() postprocess.Merger { return c.merger }

// pickMerger выбирает реализацию Merger по Options и наличию ffmpeg.
func (c *Client) pickMerger() postprocess.Merger {
	if c.opts.Merger != nil {
		return c.opts.Merger
	}
	preferFFmpeg := c.opts.PreferFFmpeg == nil || *c.opts.PreferFFmpeg
	goMux := postprocess.NewGoMerger()
	switch {
	case c.ffmpeg != nil && preferFFmpeg:
		return c.ffmpeg
	case c.ffmpeg != nil:
		// Встроенный муксер первичен, ffmpeg — запасной для контейнеров,
		// которые Go-муксер не поддерживает (например, ремукс MPEG-TS).
		goMux.Fallback = c.ffmpeg
		return goMux
	default:
		return goMux
	}
}

// maxURLDepth ограничивает цепочку KindURL-переходов.
const maxURLDepth = 8

// Extract извлекает информацию по URL, проходя KindURL-переадресации.
// Элементы плейлиста не разворачиваются (см. Walk).
func (c *Client) Extract(ctx context.Context, rawURL string) (*media.Info, error) {
	return c.extract(ctx, rawURL, "", 0)
}

func (c *Client) extract(ctx context.Context, rawURL, ieKey string, depth int) (*media.Info, error) {
	if depth > maxURLDepth {
		return nil, fmt.Errorf("too many redirections between extractors for %s", rawURL)
	}
	var ie extractor.Extractor
	if ieKey != "" {
		if e, ok := c.reg.Get(ieKey); ok && e.Suitable(rawURL) {
			ie = e
		}
	}
	if ie == nil {
		e, err := c.reg.Find(rawURL)
		if err != nil {
			return nil, err
		}
		ie = e
	}
	c.log.Debug("extracting", "extractor", ie.Name(), "url", rawURL)
	info, err := ie.Extract(ctx, c.env, rawURL)
	// экстрактор может отказаться от URL уже после проверки (например, PeerTube
	// узнаёт инстанс только запросом к API) — тогда пробуем следующие по очереди
	for tried := []string{ie.Name()}; err != nil && errors.Is(err, extractor.ErrUnsupportedURL); {
		next, ferr := c.reg.FindExcept(rawURL, tried...)
		if ferr != nil {
			break
		}
		tried = append(tried, next.Name())
		c.log.Debug("extractor declined URL, trying next", "declined", ie.Name(), "next", next.Name())
		ie = next
		info, err = ie.Extract(ctx, c.env, rawURL)
	}
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("[%s] extractor returned no result", ie.Name())
	}
	if info.Kind == media.KindURL {
		inner, err := c.extract(ctx, info.URL, info.IEKey, depth+1)
		if err != nil {
			return nil, err
		}
		mergeMissing(inner, info)
		return inner, nil
	}
	c.normalize(info, ie.Name(), rawURL)
	return info, nil
}

// mergeMissing переносит метаданные из ссылки (например, названия из плейлиста),
// если экстрактор их не дал.
func mergeMissing(dst, src *media.Info) {
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.PlaylistIndex == 0 {
		dst.PlaylistIndex = src.PlaylistIndex
		dst.PlaylistID = src.PlaylistID
		dst.Playlist = src.Playlist
	}
}

func (c *Client) normalize(info *media.Info, ie, rawURL string) {
	if info.Kind == "" {
		info.Kind = media.KindVideo
		if info.Entries != nil {
			info.Kind = media.KindPlaylist
		}
	}
	if info.Extractor == "" {
		info.Extractor = ie
	}
	if info.WebpageURL == "" {
		info.WebpageURL = rawURL
	}
	if info.Title == "" {
		info.Title = info.ID
	}
	if info.Thumbnail == "" && len(info.Thumbnails) > 0 {
		info.Thumbnail = info.Thumbnails[len(info.Thumbnails)-1].URL
	}
	for _, f := range info.Formats {
		if f.Protocol == "" {
			switch {
			case strings.HasPrefix(f.URL, "http://"):
				f.Protocol = media.ProtoHTTP
			case extractor.URLExt(f.URL) == "m3u8":
				f.Protocol = media.ProtoHLS
			default:
				f.Protocol = media.ProtoHTTPS
			}
		}
		if f.Ext == "" {
			f.Ext = extractor.URLExt(f.URL)
		}
		// ext и format_id приходят с сайта и попадают в имена файлов — только безопасные символы
		if f.Ext = safeExt(f.Ext); f.Ext == "" || f.Ext == "m3u8" {
			f.Ext = "mp4"
		}
		// примерный размер из битрейта, как в yt-dlp
		if f.Filesize == 0 && f.FilesizeApprox == 0 && info.Duration > 0 {
			br := f.TBR
			if br == 0 {
				br = f.VBR + f.ABR
			}
			f.FilesizeApprox = int64(br * 1000 / 8 * info.Duration)
		}
	}
	format.Sort(info.Formats)
}

// Walk обходит видео: для одиночного видео вызывает fn один раз, для плейлиста
// разворачивает элементы (включая вложенные плейлисты) с учётом PlaylistStart/End.
// Ошибка экстракции элемента передаётся в fn (entry == nil); если fn вернёт
// ошибку — обход прекращается.
func (c *Client) Walk(ctx context.Context, info *media.Info, fn func(entry *media.Info, err error) error) error {
	if info.Kind != media.KindPlaylist {
		return fn(info, nil)
	}
	for i, e := range info.Entries {
		idx := i + 1
		if c.opts.PlaylistStart > 0 && idx < c.opts.PlaylistStart {
			continue
		}
		if c.opts.PlaylistEnd > 0 && idx > c.opts.PlaylistEnd {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		e.PlaylistIndex = idx
		e.PlaylistID = info.ID
		e.Playlist = info.Title
		entry := e
		if e.Kind == media.KindURL || (e.Kind == "" && len(e.Formats) == 0 && e.URL != "") {
			resolved, err := c.extract(ctx, e.URL, e.IEKey, 1)
			if err != nil {
				if ferr := fn(nil, fmt.Errorf("playlist item %d: %w", idx, err)); ferr != nil {
					return ferr
				}
				continue
			}
			mergeMissing(resolved, e)
			resolved.PlaylistIndex, resolved.PlaylistID, resolved.Playlist = idx, info.ID, info.Title
			entry = resolved
		} else {
			c.normalize(entry, info.Extractor, e.WebpageURL)
		}
		if entry.Kind == media.KindPlaylist {
			if err := c.Walk(ctx, entry, fn); err != nil {
				return err
			}
			continue
		}
		if err := fn(entry, nil); err != nil {
			return err
		}
	}
	return nil
}

// defaults — настройки загрузки из Options клиента.
func (c *Client) defaults() callOpts {
	return callOpts{
		format:            c.opts.Format,
		outputDir:         c.opts.OutputDir,
		outputTemplate:    c.opts.OutputTemplate,
		mergeOutputFormat: c.opts.MergeOutputFormat,
		restrictFilenames: c.opts.RestrictFilenames,
		overwrite:         c.opts.Overwrite,
		writeSubtitles:    c.opts.WriteSubtitles,
		subtitleLangs:     c.opts.SubtitleLangs,
		progress:          c.opts.Progress,
	}
}

// resolve накладывает непустые поля Request на настройки клиента.
func (c *Client) resolve(req Request) callOpts {
	co := c.defaults()
	if req.Format != "" {
		co.format = req.Format
	}
	if req.OutputDir != "" {
		co.outputDir = req.OutputDir
	}
	if req.OutputTemplate != "" {
		co.outputTemplate = req.OutputTemplate
	}
	if req.MergeOutputFormat != "" {
		co.mergeOutputFormat = req.MergeOutputFormat
	}
	if req.RestrictFilenames != nil {
		co.restrictFilenames = *req.RestrictFilenames
	}
	if req.Overwrite != nil {
		co.overwrite = *req.Overwrite
	}
	if req.WriteSubtitles != nil {
		co.writeSubtitles = *req.WriteSubtitles
	}
	if req.SubtitleLangs != nil {
		co.subtitleLangs = req.SubtitleLangs
	}
	if req.Progress != nil {
		co.progress = req.Progress
	}
	return co
}

// DefaultFormat — выражение формата по умолчанию для клиента.
func (c *Client) DefaultFormat() string { return c.defaultFormat(c.defaults()) }

func (c *Client) defaultFormat(co callOpts) string {
	if co.format != "" {
		return co.format
	}
	if c.merger != nil {
		return "bv*+ba/b"
	}
	return "b/bv*+ba"
}

// SelectFormats выбирает форматы по Options.Format (или переданному spec).
func (c *Client) SelectFormats(info *media.Info, spec string) ([]format.Selection, error) {
	return c.selectFormats(info, spec, c.defaults())
}

func (c *Client) selectFormats(info *media.Info, spec string, co callOpts) ([]format.Selection, error) {
	if spec == "" {
		spec = c.defaultFormat(co)
	}
	sel, err := format.Compile(spec)
	if err != nil {
		return nil, err
	}
	if len(info.Formats) == 0 {
		return nil, fmt.Errorf("%s: %w", info.ID, extractor.ErrNoFormats)
	}
	sels, err := sel.Select(info.Formats)
	if err != nil {
		return nil, err
	}
	if c.merger == nil {
		for _, s := range sels {
			if len(s) > 1 {
				return nil, fmt.Errorf("format %s requires merging, but no merger is available", s.ID())
			}
		}
	}
	return sels, nil
}

// Prepare заполняет выбранные форматы, расширение и имя файла (без загрузки).
// Возвращает по копии Info на каждую выборку.
func (c *Client) Prepare(info *media.Info) ([]*media.Info, error) {
	return c.prepare(info, c.defaults())
}

// PrepareWith — как Prepare, но с переопределениями из req.
func (c *Client) PrepareWith(info *media.Info, req Request) ([]*media.Info, error) {
	return c.prepare(info, c.resolve(req))
}

func (c *Client) prepare(info *media.Info, co callOpts) ([]*media.Info, error) {
	sels, err := c.selectFormats(info, co.format, co)
	if err != nil {
		return nil, err
	}
	out := make([]*media.Info, 0, len(sels))
	for _, s := range sels {
		cp := *info
		cp.RequestedFormats = s
		cp.FormatID = s.ID()
		if len(s) > 1 {
			cp.Ext = postprocess.MergeExt(s, co.mergeOutputFormat)
		} else {
			cp.Ext = s[0].Ext
		}
		cp.Filename = outtmpl.Filename(co.outputDir, co.outputTemplate, &cp, co.restrictFilenames)
		out = append(out, &cp)
	}
	return out, nil
}

// StreamLink — прямая ссылка на поток для передачи внешнему плееру без
// скачивания. Для HLS/DASH это адрес манифеста (master .m3u8 / .mpd), чтобы
// плеер сам выбирал качество; для прогрессивных форматов — прямой URL файла.
type StreamLink struct {
	URL      string            // манифест или прямой медиа-URL
	Protocol string            // media.ProtoHLS / ProtoDASH / ProtoHTTPS ...
	Headers  map[string]string // заголовки, которые плеер должен слать при запросах
	IsLive   bool
	FormatID string // для прогрессивных ссылок (для манифеста пусто)
	Note     string
}

// StreamLinks извлекает URL (без загрузки) и возвращает прямые ссылки на
// потоки: адрес манифеста для HLS/DASH (например, Twitch и YouTube Live) или
// прямые URL прогрессивных форматов — всё с нужными заголовками. Плейлисты не
// разворачиваются (используйте Walk + StreamLinksInfo на каждый элемент).
func (c *Client) StreamLinks(ctx context.Context, rawURL string) ([]StreamLink, error) {
	info, err := c.Extract(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return c.StreamLinksInfo(info)
}

// StreamLinksInfo — как StreamLinks, но для уже извлечённой информации.
func (c *Client) StreamLinksInfo(info *media.Info) ([]StreamLink, error) {
	if info.Kind == media.KindPlaylist {
		return nil, fmt.Errorf("stream links: %s is a playlist; use Walk and StreamLinksInfo per entry", info.ID)
	}
	if len(info.Formats) == 0 {
		return nil, fmt.Errorf("%s: %w", info.ID, extractor.ErrNoFormats)
	}
	var links []StreamLink
	seen := map[string]bool{}
	// Манифесты HLS/DASH: отдаём адрес манифеста (плеер выберет качество сам),
	// схлопывая варианты с общим ManifestURL в одну ссылку.
	for _, f := range info.Formats {
		if f.Protocol != media.ProtoHLS && f.Protocol != media.ProtoDASH {
			continue
		}
		man := f.ManifestURL
		if man == "" {
			man = f.URL
		}
		if man == "" || seen[man] {
			continue
		}
		seen[man] = true
		links = append(links, StreamLink{
			URL: man, Protocol: f.Protocol, Headers: info.Headers(f),
			IsLive: info.IsLive || f.Live,
		})
	}
	if len(links) > 0 {
		return links, nil
	}
	// Прогрессивные форматы: лучший по умолчанию (предпочитая единый файл).
	sels, err := c.selectFormats(info, "b/bv*+ba", c.defaults())
	if err != nil {
		return nil, err
	}
	for _, s := range sels {
		for _, f := range s {
			if f.URL == "" || seen[f.URL] {
				continue
			}
			seen[f.URL] = true
			links = append(links, StreamLink{
				URL: f.URL, Protocol: f.Protocol, Headers: info.Headers(f),
				IsLive: info.IsLive || f.Live, FormatID: f.FormatID, Note: f.FormatNote,
			})
		}
	}
	return links, nil
}

// Result — итог загрузки одного видео.
type Result struct {
	Info     *media.Info
	Filename string
	// Skipped — файл уже существовал.
	Skipped bool
	Err     error
}

// Download извлекает и скачивает всё по URL (включая элементы плейлиста).
// Ошибки отдельных элементов попадают в Result.Err; возвращаемая ошибка —
// только фатальная (экстракция корня, отмена ctx).
func (c *Client) Download(ctx context.Context, rawURL string) ([]Result, error) {
	info, err := c.Extract(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return c.downloadInfo(ctx, info, c.defaults())
}

// DownloadWith извлекает req.URL и скачивает его с переопределениями из req
// (свой колбэк прогресса, формат, каталог) — для параллельных загрузок одним
// клиентом.
func (c *Client) DownloadWith(ctx context.Context, req Request) ([]Result, error) {
	info, err := c.Extract(ctx, req.URL)
	if err != nil {
		return nil, err
	}
	return c.downloadInfo(ctx, info, c.resolve(req))
}

// DownloadInfo скачивает уже извлечённую информацию.
func (c *Client) DownloadInfo(ctx context.Context, info *media.Info) ([]Result, error) {
	return c.downloadInfo(ctx, info, c.defaults())
}

// DownloadInfoWith — как DownloadInfo, но с переопределениями из req.
func (c *Client) DownloadInfoWith(ctx context.Context, info *media.Info, req Request) ([]Result, error) {
	return c.downloadInfo(ctx, info, c.resolve(req))
}

func (c *Client) downloadInfo(ctx context.Context, info *media.Info, co callOpts) ([]Result, error) {
	var results []Result
	err := c.Walk(ctx, info, func(e *media.Info, err error) error {
		if err != nil {
			results = append(results, Result{Err: err})
			return nil
		}
		rs, err := c.downloadVideo(ctx, e, co)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			results = append(results, Result{Info: e, Err: err})
			return nil
		}
		results = append(results, rs...)
		return nil
	})
	return results, err
}

// DownloadVideo скачивает одно видео (может дать несколько файлов при spec через ',').
func (c *Client) DownloadVideo(ctx context.Context, info *media.Info) ([]Result, error) {
	return c.downloadVideo(ctx, info, c.defaults())
}

// DownloadVideoWith — как DownloadVideo, но с переопределениями из req.
func (c *Client) DownloadVideoWith(ctx context.Context, info *media.Info, req Request) ([]Result, error) {
	return c.downloadVideo(ctx, info, c.resolve(req))
}

func (c *Client) downloadVideo(ctx context.Context, info *media.Info, co callOpts) ([]Result, error) {
	prepared, err := c.prepare(info, co)
	if err != nil {
		return nil, err
	}
	var results []Result
	for _, p := range prepared {
		r := Result{Info: p, Filename: p.Filename}
		if !co.overwrite && exists(p.Filename) {
			r.Skipped = true
			c.log.Info("already downloaded", "file", p.Filename)
		} else {
			r.Err = c.downloadPrepared(ctx, p, co)
		}
		if r.Err == nil && co.writeSubtitles {
			if err := c.writeSubtitles(ctx, p, co); err != nil {
				c.log.Warn("subtitles", "err", err)
			}
		}
		results = append(results, r)
		if ctx.Err() != nil {
			if isLive(p) && r.Err == nil {
				// запись эфира остановлена отменой ctx — это штатное завершение
				return results, nil
			}
			return results, ctx.Err()
		}
	}
	return results, nil
}

// isLive — запись живого потока (останавливается отменой ctx).
func isLive(info *media.Info) bool {
	if info.IsLive {
		return true
	}
	for _, f := range info.RequestedFormats {
		if f.Live {
			return true
		}
	}
	return false
}

func (c *Client) dlOptions(co callOpts) downloader.Options {
	return downloader.Options{
		HTTP:      c.env.HTTP,
		UserAgent: c.env.UserAgent,
		Retries:   c.opts.Retries,
		Fragments: c.opts.Fragments,
		ChunkSize: c.opts.ChunkSize,
		Progress:  co.progress,
		RateLimit: c.opts.RateLimit,
	}
}

func (c *Client) downloadPrepared(ctx context.Context, info *media.Info, co callOpts) error {
	if dir := filepath.Dir(info.Filename); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	formats := info.RequestedFormats
	// постобработка эфира выполняется и после остановки записи (отмены ctx)
	ppCtx := ctx
	if isLive(info) {
		ppCtx = context.WithoutCancel(ctx)
	}
	if len(formats) == 1 {
		return c.downloadOne(ctx, ppCtx, info, formats[0], info.Filename, true, co)
	}
	base := strings.TrimSuffix(info.Filename, filepath.Ext(info.Filename))
	inputs := make([]postprocess.Input, len(formats))
	for i, f := range formats {
		inputs[i] = postprocess.Input{Path: fmt.Sprintf("%s.f%s.%s", base, safeComponent(f.FormatID), safeExt(f.Ext)), Format: f}
	}
	if isLive(info) {
		// видео и аудио эфира пишутся одновременно, иначе дорожки разойдутся во времени
		errs := make([]error, len(formats))
		var wg sync.WaitGroup
		for i, f := range formats {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = c.downloadOne(ctx, ppCtx, info, f, inputs[i].Path, false, co)
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
	} else {
		for i, f := range formats {
			if err := c.downloadOne(ctx, ppCtx, info, f, inputs[i].Path, false, co); err != nil {
				return err
			}
		}
	}
	c.log.Info("merging formats", "file", info.Filename)
	tmp := info.Filename + ".temp" + filepath.Ext(info.Filename)
	if err := c.merger.Merge(ppCtx, tmp, inputs); err != nil {
		return err
	}
	if err := os.Rename(tmp, info.Filename); err != nil {
		return err
	}
	for _, in := range inputs {
		_ = os.Remove(in.Path)
	}
	return nil
}

// downloadOne качает формат; для HLS при необходимости делает ремукс TS → контейнер.
// ppCtx — контекст постобработки (для эфира не отменяется вместе с записью).
func (c *Client) downloadOne(ctx, ppCtx context.Context, info *media.Info, f *media.Format, dst string, final bool, co callOpts) error {
	opts := c.dlOptions(co)
	if target := c.env.ImpersonateTarget(f.Impersonate); target != "" {
		hc, err := c.env.ClientFor(target)
		if err != nil {
			return err
		}
		opts.HTTP, opts.UserAgent = hc, ""
	}
	d, err := downloader.For(f, opts)
	if err != nil {
		return err
	}
	headers := info.Headers(f)
	if f.Protocol != media.ProtoHLS {
		return d.Download(ctx, f, headers, dst)
	}
	// HLS: сегменты MPEG-TS склеиваются как есть; в mp4 их нужно перепаковать.
	tsPath := dst + ".ts"
	if err := d.Download(ctx, f, headers, tsPath); err != nil {
		return err
	}
	if err := c.merger.Remux(ppCtx, tsPath, dst); err != nil {
		if errors.Is(err, postprocess.ErrUnsupportedMux) {
			// Муксер не умеет ремуксить MPEG-TS — сохраняем поток как .ts.
			if final {
				c.log.Warn("no TS-capable merger; HLS stream saved as MPEG-TS", "file", dst)
			}
			return os.Rename(tsPath, dst)
		}
		return err
	}
	return os.Remove(tsPath)
}

func (c *Client) writeSubtitles(ctx context.Context, info *media.Info, co callOpts) error {
	base := strings.TrimSuffix(info.Filename, filepath.Ext(info.Filename))
	var errs []error
	for lang, tracks := range info.Subtitles {
		if !wantLang(co.subtitleLangs, lang) || len(tracks) == 0 {
			continue
		}
		t := tracks[0]
		ext := t.Ext
		if ext == "" {
			ext = "vtt"
		}
		if ext = safeExt(ext); ext == "" {
			ext = "vtt"
		}
		path := fmt.Sprintf("%s.%s.%s", base, safeComponent(lang), ext)
		data := []byte(t.Data)
		if t.URL != "" {
			r, err := c.env.Fetch(ctx, extractor.Request{URL: t.URL, Headers: info.HTTPHeaders})
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", lang, err))
				continue
			}
			data = r.Body
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func wantLang(langs []string, lang string) bool {
	if len(langs) == 0 {
		return true
	}
	for _, l := range langs {
		if l == "all" || strings.EqualFold(l, lang) {
			return true
		}
	}
	return false
}

// safeComponent оставляет в части имени файла только [A-Za-z0-9._-] (без
// разделителей пути и ".."), прочие символы заменяет на '_'.
func safeComponent(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		case c == '.' && i > 0 && b[i-1] != '.':
		default:
			b[i] = '_'
		}
	}
	if len(b) > 64 {
		b = b[:64]
	}
	if len(b) == 0 {
		return "_"
	}
	return string(b)
}

// safeExt — расширение: только [a-z0-9], до 10 символов ("" если не подходит).
func safeExt(s string) string {
	s = strings.ToLower(s)
	if len(s) == 0 || len(s) > 10 {
		return ""
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return ""
		}
	}
	return s
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// browserImpersonate сопоставляет браузеру из --cookies-from-browser
// (BROWSER[:PROFILE]) цель имитации. Пусто — если совпадения нет.
func browserImpersonate(spec string) string {
	browser, _, _ := strings.Cut(spec, ":")
	switch strings.ToLower(strings.TrimSpace(browser)) {
	case "chrome", "chromium", "brave", "edge", "opera", "vivaldi", "whale":
		return "chrome"
	case "firefox":
		return "firefox"
	case "safari":
		return "safari"
	}
	return ""
}
