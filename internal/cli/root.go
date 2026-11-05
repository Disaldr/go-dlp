// Package cli — командная строка go-dlp поверх библиотеки (cobra).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	dlp "github.com/Disaldr/go-dlp"
	"github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/impersonate"
	"github.com/Disaldr/go-dlp/media"
	"github.com/Disaldr/go-dlp/postprocess"
	"github.com/Disaldr/go-dlp/script"
)

type flags struct {
	format, output, paths, mergeFormat    string
	listFormats, dumpJSON, dumpSingleJSON bool
	getURL, getTitle, getFilename         bool
	simulate, skipDownload                bool
	quiet, noProgress, verbose            bool
	noPlaylist                            bool
	playlistStart, playlistEnd            int
	ffmpeg, userAgent, proxy              string
	extractorArgs                         []string
	retries, fragments                    int
	chunkSize, limitRate                  string
	forceOverwrites, restrict, ignoreErrs bool
	writeSubs                             bool
	subLangs                              []string
	listExtractors                        bool
	cookies                               string
	cookiesFromBrowser                    string
	merger                                string
	username, password, twoFactor         string
	netrc                                 bool
	netrcFile                             string
	impersonate                           string
	listImpersonate                       bool
	updateExtractors                      bool
	extractorScripts                      string
	extractorPackURL                      string
	concurrentDownloads                   int
	prints, progressTemplates             []string
	printTemplates                        []stageTemplate
	progressTemplate                      string
	extractAudio, windowsFilenames        bool
	audioFormat, remuxVideo               string
	newline, showProgress, noColors       bool
	ignoreConfig, noWarnings, noSimulate  bool
	encoding                              string
	jsRuntimes                            []string
	socketTimeout                         float64
	extractorRetries                      int
}

// NewRootCommand создаёт корневую команду.
func NewRootCommand() *cobra.Command {
	f := &flags{}
	cmd := &cobra.Command{
		Use:           "go-dlp [OPTIONS] URL [URL...]",
		Short:         "Загрузчик видео — порт yt-dlp на Go",
		Version:       dlp.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.listExtractors {
				return printExtractors(cmd.OutOrStdout())
			}
			if f.listImpersonate {
				for _, t := range impersonate.Targets {
					fmt.Fprintf(cmd.OutOrStdout(), "%-8s %-6s %-8s %s\n", t.Client, t.Version, t.OS, t.Name)
				}
				return nil
			}
			if len(args) == 0 {
				return errors.New("you must provide at least one URL (see --help)")
			}
			return run(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), f, args)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.format, "format", "f", "", `выражение выбора форматов, напр. "bv*+ba/b", "best[height<=720]"`)
	fl.StringVarP(&f.output, "output", "o", "", "шаблон имени файла (по умолчанию \"%(title)s [%(id)s].%(ext)s\")")
	fl.StringVarP(&f.paths, "paths", "P", "", "каталог для загрузки")
	fl.StringVar(&f.mergeFormat, "merge-output-format", "", "контейнер при слиянии: mp4, mkv, webm...")
	fl.StringVar(&f.merger, "merger", "auto", "муксер для слияния: auto, ffmpeg, go (встроенный на чистом Go)")
	fl.BoolVarP(&f.listFormats, "list-formats", "F", false, "показать доступные форматы")
	fl.BoolVarP(&f.dumpJSON, "dump-json", "j", false, "вывести JSON по каждому видео (без загрузки)")
	fl.BoolVarP(&f.dumpSingleJSON, "dump-single-json", "J", false, "вывести JSON целиком (плейлист — одним объектом)")
	fl.BoolVarP(&f.getURL, "get-url", "g", false, "вывести прямые ссылки")
	fl.BoolVarP(&f.getTitle, "get-title", "e", false, "вывести названия")
	fl.BoolVar(&f.getFilename, "get-filename", false, "вывести имена файлов")
	fl.BoolVarP(&f.simulate, "simulate", "s", false, "ничего не скачивать")
	fl.BoolVar(&f.skipDownload, "skip-download", false, "не скачивать видео (субтитры — можно)")
	fl.BoolVarP(&f.quiet, "quiet", "q", false, "тихий режим")
	fl.BoolVar(&f.noProgress, "no-progress", false, "не показывать прогресс")
	fl.BoolVarP(&f.verbose, "verbose", "v", false, "отладочный вывод")
	fl.BoolVar(&f.noPlaylist, "no-playlist", false, "для ссылки «видео в плейлисте» качать только видео")
	fl.IntVar(&f.playlistStart, "playlist-start", 0, "первый элемент плейлиста (с 1)")
	fl.IntVar(&f.playlistEnd, "playlist-end", 0, "последний элемент плейлиста")
	fl.StringVar(&f.ffmpeg, "ffmpeg-location", "", "путь к ffmpeg или каталогу с ним")
	fl.StringVar(&f.userAgent, "user-agent", "", "User-Agent")
	fl.StringVar(&f.proxy, "proxy", "", "прокси, напр. socks5://127.0.0.1:1080")
	fl.StringVar(&f.cookies, "cookies", "", "файл cookies: Netscape cookies.txt или JSON-экспорт расширения браузера")
	fl.StringVar(&f.cookiesFromBrowser, "cookies-from-browser", "", "читать cookies из BROWSER[:PROFILE]: chrome, chromium, firefox, brave, edge, opera, vivaldi, whale, safari")
	fl.StringVarP(&f.username, "username", "u", "", "логин для входа на сайт")
	fl.StringVarP(&f.password, "password", "p", "", "пароль для входа на сайт")
	fl.StringVar(&f.twoFactor, "twofactor", "", "код двухфакторной аутентификации")
	fl.BoolVar(&f.netrc, "netrc", false, "брать учётные данные из ~/.netrc")
	fl.StringVar(&f.netrcFile, "netrc-location", "", "путь к файлу .netrc (включает --netrc)")
	fl.StringVar(&f.impersonate, "impersonate", "", `имитировать браузер во всех запросах: any, chrome, chrome:131, safari, firefox...`)
	fl.BoolVar(&f.listImpersonate, "list-impersonate-targets", false, "список целей имитации браузера")
	fl.StringArrayVar(&f.extractorArgs, "extractor-args", nil, `аргументы экстрактора "IE:key=v1,v2;key2=v" (повторяемый)`)
	fl.IntVarP(&f.retries, "retries", "R", 3, "повторов при ошибках сети")
	fl.IntVarP(&f.fragments, "concurrent-fragments", "N", 4, "параллельных HLS-фрагментов")
	fl.StringVar(&f.chunkSize, "http-chunk-size", "", "размер Range-чанка, напр. 10M")
	fl.StringVarP(&f.limitRate, "limit-rate", "r", "", "ограничение скорости, напр. 2M")
	fl.BoolVar(&f.forceOverwrites, "force-overwrites", false, "перезаписывать файлы")
	fl.BoolVar(&f.restrict, "restrict-filenames", false, "только ASCII в именах файлов")
	fl.BoolVarP(&f.ignoreErrs, "ignore-errors", "i", false, "не завершаться с ошибкой, если упали отдельные видео")
	fl.BoolVar(&f.writeSubs, "write-subs", false, "сохранять субтитры")
	fl.StringSliceVar(&f.subLangs, "sub-langs", nil, "языки субтитров (через запятую, all — все)")
	fl.BoolVar(&f.listExtractors, "list-extractors", false, "список экстракторов")
	fl.BoolVar(&f.updateExtractors, "update-extractors", false, "скачать свежий подписанный пакет JS-экстракторов (кеш в каталоге кеша пользователя)")
	fl.StringVar(&f.extractorScripts, "extractor-scripts", "", "каталог с JS-экстракторами (*.js) для разработки, без подписи")
	fl.StringVar(&f.extractorPackURL, "extractor-pack-url", "", "URL пакета JS-экстракторов (по умолчанию официальный; подпись — URL+\".sig\")")
	fl.IntVarP(&f.concurrentDownloads, "concurrent-downloads", "M", 1, "параллельных загрузок видео (воркер-пул; >1 отключает детальный прогресс)")
	fl.StringArrayVarP(&f.prints, "print", "O", nil, "вывести [video|before_dl|after_move|after_video:]шаблон (повторяемый; включает quiet)")
	fl.StringArrayVar(&f.progressTemplates, "progress-template", nil, "шаблон прогресса [download:]TEMPLATE; поля info и progress")
	fl.BoolVarP(&f.extractAudio, "extract-audio", "x", false, "извлечь аудио через ffmpeg")
	fl.StringVar(&f.audioFormat, "audio-format", "best", "формат аудио с -x: best, mp3, m4a, opus, aac, alac, flac, vorbis, wav")
	fl.StringVar(&f.remuxVideo, "remux-video", "", "перепаковать результат в один контейнер без перекодирования")
	fl.BoolVar(&f.windowsFilenames, "windows-filenames", false, "Windows-совместимые имена файлов")
	fl.BoolVar(&f.newline, "newline", false, "прогресс отдельными строками")
	fl.BoolVar(&f.showProgress, "progress", false, "показывать прогресс даже в тихом режиме")
	fl.BoolVar(&f.noColors, "no-colors", false, "вывод без ANSI-последовательностей")
	fl.BoolVar(&f.ignoreConfig, "ignore-config", false, "не читать конфигурацию (go-dlp не читает её по умолчанию)")
	fl.BoolVar(&f.noWarnings, "no-warnings", false, "не выводить предупреждения")
	fl.BoolVar(&f.noSimulate, "no-simulate", false, "скачивать при --print video")
	fl.StringVar(&f.encoding, "encoding", "utf-8", "кодировка вывода (utf-8)")
	fl.StringArrayVar(&f.jsRuntimes, "js-runtimes", nil, "совместимость с yt-dlp; go-dlp использует встроенный JS-движок")
	fl.Float64Var(&f.socketTimeout, "socket-timeout", 0, "тайм-аут ожидания сетевых данных в секундах")
	fl.IntVar(&f.extractorRetries, "extractor-retries", 0, "повторов экстракции при временных сетевых ошибках")

	cmd.AddCommand(newExtractorsCommand())
	return cmd
}

func newExtractorsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "extractors",
		Short: "Список встроенных экстракторов",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printExtractors(cmd.OutOrStdout())
		},
	}
}

func printExtractors(w io.Writer) error {
	c, err := dlp.New(dlp.Options{})
	if err != nil {
		return err
	}
	for _, e := range c.Registry().List() {
		desc := ""
		if d, ok := e.(extractor.Describer); ok {
			desc = d.Description()
		}
		fmt.Fprintf(w, "%-16s %s\n", e.Name(), desc)
	}
	return nil
}

// ParseExtractorArgs разбирает "youtube:player_client=visionos,ios;foo=bar".
func ParseExtractorArgs(list []string) (map[string]map[string][]string, error) {
	out := map[string]map[string][]string{}
	for _, s := range list {
		ie, rest, ok := strings.Cut(s, ":")
		if !ok || ie == "" {
			return nil, fmt.Errorf("invalid --extractor-args %q (expected IE:key=value)", s)
		}
		ie = strings.ToLower(ie)
		if out[ie] == nil {
			out[ie] = map[string][]string{}
		}
		for _, kv := range strings.Split(rest, ";") {
			k, v, _ := strings.Cut(kv, "=")
			if strings.TrimSpace(k) == "" {
				return nil, fmt.Errorf("invalid extractor arg %q", kv)
			}
			out[ie][strings.TrimSpace(k)] = strings.Split(v, ",")
		}
	}
	return out, nil
}

// ParseSize разбирает "10M", "512K", "1.5G".
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, nil
	}
	mult := 1.0
	for _, suf := range []struct {
		s string
		m float64
	}{{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"B", 1}} {
		if strings.HasSuffix(s, suf.s) {
			mult = suf.m
			s = strings.TrimSuffix(s, suf.s)
			break
		}
	}
	var v float64
	if _, err := fmt.Sscanf(s, "%g", &v); err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return int64(v * mult), nil
}

func run(ctx context.Context, stdout, stderr io.Writer, f *flags, urls []string) error {
	if err := f.prepareOutput(); err != nil {
		return err
	}
	// Все воркеры и колбэки пишут целые строки под общей блокировкой.
	var outputMu sync.Mutex
	stdout = &lockedWriter{mu: &outputMu, w: stdout}
	stderr = &lockedWriter{mu: &outputMu, w: stderr}
	level := slog.LevelInfo
	switch {
	case f.verbose:
		level = slog.LevelDebug
	case f.quiet:
		level = slog.LevelError
	}
	var handler slog.Handler = slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level})
	if f.noWarnings {
		handler = &noWarningsHandler{Handler: handler}
	}
	logger := slog.New(handler)

	eargs, err := ParseExtractorArgs(f.extractorArgs)
	if err != nil {
		return err
	}
	chunk, err := ParseSize(f.chunkSize)
	if err != nil {
		return err
	}
	rate, err := ParseSize(f.limitRate)
	if err != nil {
		return err
	}
	printOnly := f.dumpJSON || f.dumpSingleJSON || f.getURL || f.getTitle || f.getFilename || f.listFormats
	if f.printOnly() && !f.noSimulate {
		printOnly = true
	}
	workers := f.concurrentDownloads
	if workers < 1 {
		workers = 1
	}
	// Параллелим только реальную загрузку: режимы вывода и симуляции должны
	// сохранять порядок строк и идут последовательно.
	parallel := workers > 1 && !printOnly && !f.simulate && !f.skipDownload
	var progress *progressPrinter
	// Единая строка прогресса (\r) рассчитана на одну активную загрузку — при
	// нескольких воркерах её заменяют построчные сообщения по каждому видео.
	if (!f.quiet || f.showProgress) && !f.noProgress && !printOnly && !parallel && f.progressTemplate == "" {
		progress = newProgressPrinter(stderr)
		progress.newline, progress.noColors = f.newline, f.noColors
	}
	var customMerger postprocess.Merger
	var preferFFmpeg *bool
	switch f.merger {
	case "go":
		customMerger = postprocess.NewGoMerger() // чистый Go, без ffmpeg
	case "ffmpeg":
		t := true
		preferFFmpeg = &t
	}
	opts := dlp.Options{
		Merger:              customMerger,
		PreferFFmpeg:        preferFFmpeg,
		Format:              f.format,
		OutputTemplate:      f.output,
		OutputDir:           f.paths,
		RestrictFilenames:   f.restrict,
		MergeOutputFormat:   f.mergeFormat,
		ExtractAudio:        f.extractAudio,
		AudioFormat:         f.audioFormat,
		RemuxVideo:          f.remuxVideo,
		WindowsFilenames:    f.windowsFilenames,
		SocketTimeout:       time.Duration(f.socketTimeout * float64(time.Second)),
		ExtractorRetries:    f.extractorRetries,
		Overwrite:           f.forceOverwrites,
		ExtractorArgs:       eargs,
		Proxy:               f.proxy,
		CookiesFile:         f.cookies,
		CookiesFromBrowser:  f.cookiesFromBrowser,
		Username:            f.username,
		Password:            f.password,
		TwoFactor:           f.twoFactor,
		Netrc:               f.netrc,
		NetrcFile:           f.netrcFile,
		ExtractorScriptsDir: f.extractorScripts,
		Impersonate:         f.impersonate,
		UserAgent:           f.userAgent,
		FFmpegLocation:      f.ffmpeg,
		NoPlaylist:          f.noPlaylist,
		PlaylistStart:       f.playlistStart,
		PlaylistEnd:         f.playlistEnd,
		Retries:             f.retries,
		Fragments:           f.fragments,
		ChunkSize:           chunk,
		RateLimit:           rate,
		WriteSubtitles:      f.writeSubs,
		SubtitleLangs:       f.subLangs,
		Logger:              logger,
	}
	if progress != nil {
		opts.Progress = progress.update
	}
	client, err := dlp.New(opts)
	if err != nil {
		return err
	}
	// Ранее скачанный пакет поднимается из кеша всегда (без сети);
	// проверка обновлений — только по --update-extractors.
	u := &script.Updater{URL: f.extractorPackURL, Logger: opts.Logger, HTTP: client.Env().HTTP}
	if dir, err := os.UserCacheDir(); err == nil {
		u.CacheDir = filepath.Join(dir, "go-dlp", "extractors")
	}
	if f.extractorScripts == "" {
		if _, err := u.LoadCache(client.Scripts()); err != nil && !f.noWarnings {
			fmt.Fprintln(stderr, "WARNING: extractor pack cache:", err)
		}
	}
	if f.updateExtractors {
		if _, err := u.Check(ctx, client.Scripts()); err != nil && !f.noWarnings {
			fmt.Fprintln(stderr, "WARNING: extractor update failed:", err)
		}
		fmt.Fprintf(stderr, "[go-dlp] extractor pack version %d\n", client.Scripts().Version())
	}

	var mu sync.Mutex
	failed := 0
	report := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		failed++
		fmt.Fprintln(stderr, "ERROR:", err)
	}
	process := func(e *media.Info) {
		if err := handleVideo(ctx, client, stdout, stderr, f, e, printOnly, progress); err != nil {
			if ctx.Err() != nil {
				return
			}
			report(fmt.Errorf("%s: %w", e.ID, err))
		}
	}

	// Воркер-пул: при parallel загрузки идут в workers горутин, иначе работа
	// выполняется синхронно прямо в продюсере (поведение по умолчанию).
	var (
		jobs chan *media.Info
		wg   sync.WaitGroup
	)
	if parallel {
		jobs = make(chan *media.Info)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for e := range jobs {
					if ctx.Err() != nil {
						continue // отмена: дренируем очередь, новые не начинаем
					}
					process(e)
				}
			}()
		}
	}
	// emit отправляет готовый элемент воркерам (parallel) либо обрабатывает его
	// на месте (последовательный режим).
	emit := func(e *media.Info, err error) error {
		if err != nil {
			report(err)
			return nil
		}
		if !parallel {
			process(e)
			return nil
		}
		select {
		case jobs <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var fatal error
	for _, u := range urls {
		if ctx.Err() != nil {
			break
		}
		info, err := client.Extract(ctx, u)
		if err != nil {
			report(err)
			continue
		}
		if f.dumpSingleJSON {
			if err := dumpSingle(ctx, client, stdout, info); err != nil {
				report(err)
			}
			continue
		}
		if err := client.Walk(ctx, info, func(e *media.Info, err error) error {
			return emit(e, err)
		}); err != nil {
			fatal = err
			break
		}
	}
	if parallel {
		close(jobs)
		wg.Wait()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if fatal != nil {
		return fatal
	}
	if failed > 0 && !f.ignoreErrs {
		return fmt.Errorf("%d error(s)", failed)
	}
	return nil
}

func handleVideo(ctx context.Context, c *dlp.Client, stdout, stderr io.Writer, f *flags, info *media.Info, printOnly bool, pp *progressPrinter) error {
	if f.listFormats {
		printFormats(stdout, info)
		return nil
	}
	prepared, err := c.Prepare(info)
	if err != nil {
		return err
	}
	for _, p := range prepared {
		if err := f.printStage(stdout, "video", p); err != nil {
			return err
		}
		if f.getTitle {
			fmt.Fprintln(stdout, p.Title)
		}
		if f.getURL {
			for _, rf := range p.RequestedFormats {
				fmt.Fprintln(stdout, rf.URL)
			}
		}
		if f.getFilename {
			fmt.Fprintln(stdout, p.Filename)
		}
		if f.dumpJSON {
			b, err := json.Marshal(p)
			if err != nil {
				return err
			}
			fmt.Fprintln(stdout, string(b))
		}
	}
	if printOnly || f.simulate || f.skipDownload {
		return nil
	}
	if !f.quiet {
		fmt.Fprintf(stderr, "[%s] %s: %s (format %s)\n", info.Extractor, info.ID, info.Title, prepared[0].FormatID)
	}
	for _, p := range prepared {
		if err := f.printStage(stdout, "before_dl", p); err != nil {
			return err
		}
	}
	req := dlp.Request{}
	if f.progressTemplate != "" && (!f.quiet || f.showProgress) && !f.noProgress {
		printer := &progressPrinter{w: stdout, template: f.progressTemplate, info: info}
		req.Progress = printer.update
	}
	results, err := c.DownloadVideoWith(ctx, info, req)
	if pp != nil {
		pp.done()
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range results {
		if r.Err == nil {
			for _, stage := range []string{"after_move", "after_video"} {
				if err := f.printStage(stdout, stage, r.Info); err != nil {
					errs = append(errs, err)
				}
			}
		}
		switch {
		case r.Err != nil:
			errs = append(errs, r.Err)
		case r.Skipped && !f.quiet:
			fmt.Fprintf(stderr, "[download] %s has already been downloaded\n", r.Filename)
		case !f.quiet:
			fmt.Fprintf(stderr, "[download] Destination: %s\n", r.Filename)
		}
	}
	return errors.Join(errs...)
}

func dumpSingle(ctx context.Context, c *dlp.Client, w io.Writer, info *media.Info) error {
	if info.Kind == media.KindPlaylist {
		var entries []*media.Info
		err := c.Walk(ctx, info, func(e *media.Info, err error) error {
			if err == nil {
				entries = append(entries, e)
			}
			return nil
		})
		if err != nil {
			return err
		}
		cp := *info
		cp.Entries = entries
		info = &cp
	}
	enc := json.NewEncoder(w)
	return enc.Encode(info)
}

// Execute запускает CLI.
func Execute(ctx context.Context) int {
	cmd := NewRootCommand()
	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	return 0
}
