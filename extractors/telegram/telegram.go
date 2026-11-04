// Package telegram — экстрактор видео из публичных постов Telegram-каналов
// (t.me/<канал>/<id>) через встраиваемый виджет.
package telegram

import (
	"context"
	"net/url"
	"path"
	"regexp"
	"strings"

	ex "github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

const ieKey = "telegram:embed"

var matcher = ex.MustMatcher(`^https?://t\.me/(?:s/)?(?P<channel>[^/]+)/(?P<id>\d+)`)

// Telegram — экстрактор постов Telegram.
type Telegram struct{}

// New возвращает экстрактор Telegram.
func New() *Telegram { return &Telegram{} }

func (*Telegram) Name() string           { return ieKey }
func (*Telegram) Description() string    { return "Telegram public channel posts (video)" }
func (*Telegram) Suitable(u string) bool { return matcher.Match(u) }

var (
	reVideoBlock = regexp.MustCompile(`(?s)<a class="tgme_widget_message_video_player.+?</time>`)
	reVideoSrc   = regexp.MustCompile(`<video[^>]+src="([^"]+)"`)
	reHref       = regexp.MustCompile(`<a class="tgme_widget_message_video_player[^>]+href="([^"]+)"`)
	reDuration   = regexp.MustCompile(`<time[^>]+duration[^>]*>([\d:]+)</time>`)
	reThumb      = regexp.MustCompile(`tgme_widget_message_video_thumb"[^>]+background-image:url\('([^']+)'\)`)
	reDatetime   = regexp.MustCompile(`<time[^>]*datetime="([^"]*)"`)
)

func byClass(page, class string) string {
	re := regexp.MustCompile(`(?s)<(\w+)[^>]+class="[^"]*\b` + regexp.QuoteMeta(class) + `\b[^"]*"[^>]*>(.*?)</(?:div|span|a)>`)
	if m := re.FindStringSubmatch(page); m != nil {
		return strings.ReplaceAll(ex.CleanHTML(m[2]), "\n", " ")
	}
	return ""
}

func (t *Telegram) Extract(ctx context.Context, env *ex.Env, rawURL string) (*media.Info, error) {
	g, ok := matcher.Groups(rawURL)
	if !ok {
		return nil, ex.ErrUnsupportedURL
	}
	channel, msgID := g["channel"], g["id"]
	single := false
	if pu, err := url.Parse(rawURL); err == nil {
		_, single = pu.Query()["single"]
	}
	embed, err := env.GetText(ctx, "https://t.me/"+channel+"/"+msgID+"?embed=1&single", nil)
	if err != nil {
		return nil, ex.Wrap(ieKey, msgID, err, "unable to download embed")
	}
	if strings.Contains(embed, "tgme_widget_message_error") {
		return nil, ex.Errorf(ieKey, msgID, ex.ErrUnavailable, "%s", byClass(embed, "tgme_widget_message_error"))
	}
	desc := byClass(embed, "tgme_widget_message_text")
	author := byClass(embed, "tgme_widget_message_author")
	date := ""
	if m := reDatetime.FindStringSubmatch(embed); m != nil {
		date = ex.UnifiedDate(m[1])
	}
	var videos []*media.Info
	for _, block := range reVideoBlock.FindAllString(embed, -1) {
		src := reVideoSrc.FindStringSubmatch(block)
		href := reHref.FindStringSubmatch(block)
		if src == nil || href == nil {
			continue
		}
		id := path.Base(strings.SplitN(href[1], "?", 2)[0])
		v := &media.Info{
			Kind:        media.KindVideo,
			ID:          id,
			Title:       truncate(desc, 72),
			Description: desc,
			Channel:     author,
			Uploader:    author,
			ChannelID:   channel,
			UploadDate:  date,
			WebpageURL:  "https://t.me/" + channel + "/" + id + "?single",
			Formats:     []*media.Format{{FormatID: "mp4", URL: src[1], Ext: "mp4"}},
		}
		if v.Title == "" {
			v.Title = channel + " " + id
		}
		if m := reDuration.FindStringSubmatch(block); m != nil {
			v.Duration = ex.ParseDuration(m[1])
		}
		if m := reThumb.FindStringSubmatch(block); m != nil {
			v.Thumbnail = m[1]
		}
		videos = append(videos, v)
	}
	if len(videos) == 0 {
		return nil, ex.Errorf(ieKey, msgID, ex.ErrNoFormats, "no video in this post (or channel is private)")
	}
	// альбом из нескольких видео — плейлист, если не запрошен ?single
	if len(videos) > 1 && !single {
		title := author + " " + msgID
		return &media.Info{
			Kind: media.KindPlaylist, ID: channel + "-" + msgID, Title: strings.TrimSpace(title), Description: desc,
			Entries: videos, PlaylistCount: len(videos), WebpageURL: rawURL,
		}, nil
	}
	for _, v := range videos {
		if v.ID == msgID {
			return v, nil
		}
	}
	return videos[0], nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
