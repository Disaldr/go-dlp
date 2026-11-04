// Package media описывает результат работы экстракторов: метаданные
// видео/плейлиста и список доступных форматов (аналог info dict в yt-dlp).
package media

import (
	"fmt"
	"maps"
	"strings"
)

// Kind — тип результата экстракции.
type Kind string

const (
	// KindVideo — одиночное медиа с форматами.
	KindVideo Kind = "video"
	// KindPlaylist — набор Entries.
	KindPlaylist Kind = "playlist"
	// KindURL — ссылка, которую надо извлечь повторно (возможно, другим экстрактором).
	KindURL Kind = "url"
)

// Info — результат экстракции.
type Info struct {
	Kind Kind `json:"_type,omitempty"`

	ID          string `json:"id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// URL используется для KindURL — адрес для повторной экстракции.
	URL string `json:"url,omitempty"`
	// IEKey — предпочтительный экстрактор для URL (необязательно).
	IEKey string `json:"ie_key,omitempty"`

	WebpageURL string `json:"webpage_url,omitempty"`
	Extractor  string `json:"extractor,omitempty"`

	Uploader    string `json:"uploader,omitempty"`
	UploaderID  string `json:"uploader_id,omitempty"`
	UploaderURL string `json:"uploader_url,omitempty"`
	Channel     string `json:"channel,omitempty"`
	ChannelID   string `json:"channel_id,omitempty"`
	ChannelURL  string `json:"channel_url,omitempty"`

	// UploadDate в формате YYYYMMDD.
	UploadDate string  `json:"upload_date,omitempty"`
	Timestamp  int64   `json:"timestamp,omitempty"`
	Duration   float64 `json:"duration,omitempty"`
	ViewCount  int64   `json:"view_count,omitempty"`
	LikeCount  int64   `json:"like_count,omitempty"`
	AgeLimit   int     `json:"age_limit,omitempty"`
	IsLive     bool    `json:"is_live,omitempty"`

	Tags       []string `json:"tags,omitempty"`
	Categories []string `json:"categories,omitempty"`

	Thumbnail  string      `json:"thumbnail,omitempty"`
	Thumbnails []Thumbnail `json:"thumbnails,omitempty"`

	Chapters          []Chapter             `json:"chapters,omitempty"`
	Formats           []*Format             `json:"formats,omitempty"`
	Subtitles         map[string][]Subtitle `json:"subtitles,omitempty"`
	AutomaticCaptions map[string][]Subtitle `json:"automatic_captions,omitempty"`

	// HTTPHeaders применяются ко всем форматам, у которых нет своих заголовков.
	HTTPHeaders map[string]string `json:"http_headers,omitempty"`

	// Плейлист.
	Entries       []*Info `json:"entries,omitempty"`
	PlaylistCount int     `json:"playlist_count,omitempty"`

	// Заполняется при обходе плейлиста.
	PlaylistID    string `json:"playlist_id,omitempty"`
	Playlist      string `json:"playlist,omitempty"`
	PlaylistIndex int    `json:"playlist_index,omitempty"`

	// Заполняется после выбора форматов.
	FormatID         string    `json:"format_id,omitempty"`
	Ext              string    `json:"ext,omitempty"`
	RequestedFormats []*Format `json:"requested_formats,omitempty"`
	Filename         string    `json:"filename,omitempty"`
}

// Chapter — глава видео (секунды от начала).
type Chapter struct {
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	Title     string  `json:"title,omitempty"`
}

// Thumbnail — превью.
type Thumbnail struct {
	ID     string `json:"id,omitempty"`
	URL    string `json:"url"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// Subtitle — дорожка субтитров.
type Subtitle struct {
	URL  string `json:"url,omitempty"`
	Ext  string `json:"ext,omitempty"`
	Name string `json:"name,omitempty"`
	Data string `json:"data,omitempty"`
}

// Протоколы загрузки.
const (
	ProtoHTTPS = "https"
	ProtoHTTP  = "http"
	ProtoHLS   = "m3u8_native"
	ProtoDASH  = "http_dash_segments"
)

// CodecNone означает отсутствие потока (как "none" в yt-dlp).
const CodecNone = "none"

// Format — один вариант медиа.
type Format struct {
	FormatID    string `json:"format_id"`
	FormatNote  string `json:"format_note,omitempty"`
	URL         string `json:"url"`
	ManifestURL string `json:"manifest_url,omitempty"`
	Ext         string `json:"ext,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Container   string `json:"container,omitempty"`

	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
	FPS    float64 `json:"fps,omitempty"`

	// VCodec/ACodec: "" — неизвестно, "none" — потока нет.
	VCodec        string `json:"vcodec,omitempty"`
	ACodec        string `json:"acodec,omitempty"`
	DynamicRange  string `json:"dynamic_range,omitempty"`
	AudioChannels int    `json:"audio_channels,omitempty"`
	ASR           int    `json:"asr,omitempty"`

	// Битрейты в кбит/с.
	TBR float64 `json:"tbr,omitempty"`
	VBR float64 `json:"vbr,omitempty"`
	ABR float64 `json:"abr,omitempty"`

	Filesize       int64 `json:"filesize,omitempty"`
	FilesizeApprox int64 `json:"filesize_approx,omitempty"`

	Language string `json:"language,omitempty"`
	// LanguagePreference: больше — лучше (оригинальная дорожка и т.п.).
	LanguagePreference int `json:"language_preference,omitempty"`
	// Quality — оценка качества от экстрактора, больше — лучше.
	Quality float64 `json:"quality,omitempty"`
	// Preference — общий приоритет формата, больше — лучше.
	Preference int `json:"preference,omitempty"`

	HTTPHeaders map[string]string `json:"http_headers,omitempty"`
	// HTTPChunkSize > 0 — качать Range-запросами такого размера (нужно YouTube).
	HTTPChunkSize int64 `json:"-"`
	// Impersonate — загружать с имитацией браузера (цель, напр. "any").
	Impersonate string `json:"impersonate,omitempty"`
	// Fragments — сегменты для фрагментированных протоколов (DASH);
	// первый может быть init-сегментом.
	Fragments []Fragment `json:"-"`
	// Live — живой поток: загрузчик перечитывает манифест (ManifestURL)
	// и докачивает новые сегменты до остановки (отмены ctx) или конца эфира.
	Live bool `json:"is_live,omitempty"`
	// DASHRepresentation — id представления в MPD (для обновления живого манифеста).
	DASHRepresentation string `json:"-"`
}

// Fragment — фрагмент медиа: URL и, при необходимости, диапазон байт.
type Fragment struct {
	URL      string
	Duration float64
	// Init — инициализирующий сегмент (при обновлении живого потока не повторяется).
	Init bool
	// Length > 0 — диапазон [Offset, Offset+Length).
	Offset, Length int64
}

// HasVideo сообщает, может ли формат содержать видео.
func (f *Format) HasVideo() bool { return f.VCodec != CodecNone }

// HasAudio сообщает, может ли формат содержать аудио.
func (f *Format) HasAudio() bool { return f.ACodec != CodecNone }

// IsVideoOnly — только видео.
func (f *Format) IsVideoOnly() bool { return f.HasVideo() && f.ACodec == CodecNone }

// IsAudioOnly — только аудио.
func (f *Format) IsAudioOnly() bool { return f.HasAudio() && f.VCodec == CodecNone }

// Resolution — строка вида "1920x1080", "720p" или "audio only".
func (f *Format) Resolution() string {
	switch {
	case f.VCodec == CodecNone:
		return "audio only"
	case f.Width > 0 && f.Height > 0:
		return fmt.Sprintf("%dx%d", f.Width, f.Height)
	case f.Height > 0:
		return fmt.Sprintf("%dp", f.Height)
	case f.Width > 0:
		return fmt.Sprintf("%dx?", f.Width)
	}
	return "unknown"
}

// Size возвращает известный или примерный размер.
func (f *Format) Size() int64 {
	if f.Filesize > 0 {
		return f.Filesize
	}
	return f.FilesizeApprox
}

// Describe — человекочитаемое описание формата.
func (f *Format) Describe() string {
	parts := []string{f.FormatID, f.Ext, f.Resolution()}
	if f.FormatNote != "" {
		parts = append(parts, f.FormatNote)
	}
	return strings.Join(parts, " - ")
}

// Headers объединяет заголовки формата и info (формат приоритетнее).
func (i *Info) Headers(f *Format) map[string]string {
	h := make(map[string]string, len(i.HTTPHeaders)+len(f.HTTPHeaders))
	maps.Copy(h, i.HTTPHeaders)
	maps.Copy(h, f.HTTPHeaders)
	return h
}

// NewURL создаёт результат KindURL.
func NewURL(u, ieKey string) *Info {
	return &Info{Kind: KindURL, URL: u, IEKey: ieKey}
}
