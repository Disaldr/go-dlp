package all_test

import (
	"context"
	"regexp"
	"testing"

	ex "github.com/Disaldr/go-dlp/extractor"

	et "github.com/Disaldr/go-dlp/extractor/extractortest"
	"github.com/Disaldr/go-dlp/extractors/archiveorg"
	"github.com/Disaldr/go-dlp/extractors/aylo"
	"github.com/Disaldr/go-dlp/extractors/bandcamp"
	"github.com/Disaldr/go-dlp/extractors/bluesky"
	"github.com/Disaldr/go-dlp/extractors/coub"
	"github.com/Disaldr/go-dlp/extractors/dailymotion"
	"github.com/Disaldr/go-dlp/extractors/eporner"
	"github.com/Disaldr/go-dlp/extractors/generic"
	"github.com/Disaldr/go-dlp/extractors/kick"
	"github.com/Disaldr/go-dlp/extractors/okru"
	"github.com/Disaldr/go-dlp/extractors/peertube"
	"github.com/Disaldr/go-dlp/extractors/rutube"
	"github.com/Disaldr/go-dlp/extractors/soundcloud"
	"github.com/Disaldr/go-dlp/extractors/streamable"
	"github.com/Disaldr/go-dlp/extractors/telegram"
	"github.com/Disaldr/go-dlp/extractors/twitch"
	"github.com/Disaldr/go-dlp/extractors/vk"
	"github.com/Disaldr/go-dlp/extractors/xhamster"
	"github.com/Disaldr/go-dlp/extractors/xvideos"
	"github.com/Disaldr/go-dlp/extractors/youtube"
	"github.com/Disaldr/go-dlp/media"
)

// Онлайн-тесты: GODLP_ONLINE=1 go test ./extractors/all -run Online -v
// Данные взяты из _TESTS соответствующих экстракторов yt-dlp.

func TestOnlineYouTube(t *testing.T) {
	et.Online(t)
	t.Run("video", func(t *testing.T) {
		et.Extract(t, youtube.New(), "https://www.youtube.com/watch?v=jNQXAC9IVRw", et.Expect{
			ID: "jNQXAC9IVRw", Title: "Me at the zoo", Uploader: "jawed", MinFormats: 5, MinDuration: 18,
		})
	})
	t.Run("playlist", func(t *testing.T) {
		et.Extract(t, youtube.NewTab(), "https://www.youtube.com/playlist?list=PLBCF2DAC6FFB574DE", et.Expect{
			ID: "PLBCF2DAC6FFB574DE", Kind: media.KindPlaylist, MinEntries: 10,
		})
	})
	t.Run("search", func(t *testing.T) {
		et.Extract(t, youtube.NewSearch(), "ytsearch3:golang", et.Expect{Kind: media.KindPlaylist, MinEntries: 3})
	})
}

func TestOnlineRutube(t *testing.T) {
	et.Online(t)
	t.Run("video", func(t *testing.T) {
		et.Extract(t, rutube.New(), "https://rutube.ru/video/3eac3b4561676c17df9132a9a1e62e3e/", et.Expect{
			ID: "3eac3b4561676c17df9132a9a1e62e3e", Title: "Раненный кенгуру забежал в аптеку",
			Uploader: "NTDRussian", MinFormats: 1, MinDuration: 80,
		})
	})
	t.Run("channel", func(t *testing.T) {
		et.Extract(t, rutube.NewPlaylist(), "https://rutube.ru/channel/639184/videos/", et.Expect{
			ID: "639184_videos", Kind: media.KindPlaylist, MinEntries: 100,
		})
	})
}

func TestOnlineTwitch(t *testing.T) {
	et.Online(t)
	t.Run("clip", func(t *testing.T) {
		et.Extract(t, twitch.NewClip(), "https://clips.twitch.tv/FaintLightGullWholeWheat", et.Expect{
			ID: "396245304", Title: "re:.+", MinFormats: 1,
		})
	})
	t.Run("videos+vod", func(t *testing.T) {
		// записи живут ограниченное время — берём свежую с канала
		list := et.Extract(t, twitch.NewVideos(), "https://www.twitch.tv/riotgames/videos", et.Expect{
			Kind: media.KindPlaylist, MinEntries: 1,
		})
		et.Extract(t, twitch.NewVOD(), list.Entries[0].URL, et.Expect{ID: "re:^v\\d+$", MinFormats: 2})
	})
}

func TestOnlineSoundCloud(t *testing.T) {
	et.Online(t)
	t.Run("track", func(t *testing.T) {
		et.Extract(t, soundcloud.New(), "https://soundcloud.com/ethmusic/lostin-powers-she-so-heavy", et.Expect{
			ID: "62986583", Title: "re:^Lostin Powers - She so Heavy", Uploader: "E.T. ExTerrestrial Music", MinFormats: 1, MinDuration: 140,
		})
	})
	t.Run("set", func(t *testing.T) {
		et.Extract(t, soundcloud.NewSet(), "https://soundcloud.com/the-concept-band/sets/the-royal-concept-ep", et.Expect{
			ID: "2284613", Kind: media.KindPlaylist, MinEntries: 5,
		})
	})
	t.Run("user", func(t *testing.T) {
		et.Extract(t, soundcloud.NewUser(), "https://soundcloud.com/ethmusic", et.Expect{
			Kind: media.KindPlaylist, MinEntries: 2,
		})
	})
}

func TestOnlineStreamable(t *testing.T) {
	et.Online(t)
	et.Extract(t, streamable.New(), "https://streamable.com/dnd1", et.Expect{
		ID: "dnd1", Title: "Mikel Oiarzabal scores to make it 0-3 for La Real against Espanyol", MinFormats: 1,
	})
}

func TestOnlineDailymotion(t *testing.T) {
	et.Online(t)
	// CDN отвечает 403 без имитации браузера — проверяем, что запасной путь работает
	info := et.Extract(t, dailymotion.New(), "https://www.dailymotion.com/video/x5kesuj", et.Expect{
		ID: "x5kesuj", MinFormats: 3,
	})
	if info.Formats[0].Impersonate == "" {
		t.Log("manifest was fetched without impersonation")
	}
}

func TestOnlineGenericDASH(t *testing.T) {
	et.Online(t)
	info := et.Extract(t, generic.New(), "https://dash.akamaized.net/akamai/bbb_30fps/bbb_30fps.mpd", et.Expect{
		ID: "bbb_30fps", MinFormats: 5,
	})
	for _, f := range info.Formats {
		if f.Protocol != media.ProtoDASH || len(f.Fragments) < 100 {
			t.Errorf("format %s: protocol %s, %d fragments", f.FormatID, f.Protocol, len(f.Fragments))
		}
	}
}

func TestOnlineVK(t *testing.T) {
	et.Online(t)
	t.Run("video", func(t *testing.T) {
		et.Extract(t, vk.New(), "https://vk.com/videos-77521?z=video-77521_162222515%2Fclub77521", et.Expect{
			ID: "-77521_162222515", Title: "ProtivoGunz - Хуёвая песня", MinFormats: 5, MinDuration: 190,
		})
	})
	t.Run("embed-spa", func(t *testing.T) {
		// новый SPA-embed без playerParams: запасной путь через al_video
		et.Extract(t, vk.New(), "https://vk.com/video_ext.php?oid=-77521&id=162222515&hash=87b046504ccd8bfa", et.Expect{
			ID: "-77521_162222515", MinFormats: 5,
		})
	})
}

func TestOnlineOK(t *testing.T) {
	et.Online(t)
	info := et.Extract(t, okru.New(), "https://ok.ru/video/20079905452", et.Expect{ID: "20079905452", MinFormats: 5})
	for _, f := range info.Formats {
		if f.FormatID == "hd" && f.Height != 720 {
			t.Errorf("hd height = %d", f.Height)
		}
	}
}

func TestOnlineLiveDASH(t *testing.T) {
	et.Online(t)
	info := et.Extract(t, generic.New(), "https://livesim2.dashif.org/livesim2/testpic_2s/Manifest.mpd", et.Expect{MinFormats: 2})
	if !info.IsLive || !info.Formats[0].Live {
		t.Error("live manifest must be marked as live")
	}
}

func TestOnlineCoub(t *testing.T) {
	et.Online(t)
	et.Extract(t, coub.New(), "https://coub.com/view/5u5n1", et.Expect{ID: "5u5n1", Title: "The Matrix Moonwalk", MinFormats: 2})
}

func TestOnlineBluesky(t *testing.T) {
	et.Online(t)
	et.Extract(t, bluesky.New(), "https://bsky.app/profile/bsky.app/post/3l3vgf77uco2g", et.Expect{
		ID: "3l3vgf77uco2g", Uploader: "re:.+", MinFormats: 2,
	})
}

func TestOnlineBandcamp(t *testing.T) {
	et.Online(t)
	t.Run("track", func(t *testing.T) {
		et.Extract(t, bandcamp.NewTrack(), "https://youtube-dl.bandcamp.com/track/youtube-dl-test-song", et.Expect{
			ID: "1812978515", Title: `re:^youtube-dl\s+".+ - youtube-dl\s+".+ - youtube-dl test song`, MinFormats: 1,
		})
	})
	t.Run("album", func(t *testing.T) {
		et.Extract(t, bandcamp.NewAlbum(), "https://nightbringer.bandcamp.com/album/hierophany-of-the-open-grave", et.Expect{
			Kind: media.KindPlaylist, MinEntries: 5,
		})
	})
}

func TestOnlineArchiveOrg(t *testing.T) {
	et.Online(t)
	// исходник — ISO-образ, видео есть только в производных
	et.Extract(t, archiveorg.New(), "https://archive.org/details/XD300-23_68HighlightsAResearchCntAugHumanIntellect", et.Expect{
		ID: "XD300-23_68HighlightsAResearchCntAugHumanIntellect", Title: "1968 Demo - FJCC Conference Presentation Reel #1", MinFormats: 2,
	})
	et.Extract(t, archiveorg.New(), "https://archive.org/details/Cops1922", et.Expect{ID: "Cops1922", MinFormats: 2})
}

func TestOnlinePeerTube(t *testing.T) {
	et.Online(t)
	et.Extract(t, peertube.New(), "https://framatube.org/w/kkGMgK9ZtnKfYAgnEtQxbv", et.Expect{
		ID: "9c9de5e8-0a1e-484a-b099-e80766180a6d", Title: "What is PeerTube?", MinFormats: 4,
	})
}

func TestOnlineKick(t *testing.T) {
	et.Online(t)
	list := et.Extract(t, kick.NewVideos(), "https://kick.com/xqc/videos", et.Expect{
		ID: "xqc", Title: "xQc - Videos sorted by Date", Kind: media.KindPlaylist, MinEntries: 10,
	})
	et.Extract(t, kick.NewVOD(), list.Entries[0].URL, et.Expect{MinFormats: 2})
}

func TestOnlineTelegram(t *testing.T) {
	et.Online(t)
	et.Extract(t, telegram.New(), "https://t.me/europa_press/613", et.Expect{
		ID: "613", Title: "re:^Así de rápido fluye la lava en La Palma", MinFormats: 1, MinDuration: 60,
	})
}

func TestOnlineVKVideos(t *testing.T) {
	et.Online(t)
	et.Extract(t, vk.NewVideos(), "https://vk.com/video/@mobidevices", et.Expect{
		ID: "-17892518_all", Kind: media.KindPlaylist, MinEntries: 1000,
	})
}

// 18+: YouPorn не проверяется — доступность зависит от региона.
func TestOnlineAdult(t *testing.T) {
	et.Online(t)
	t.Run("xvideos", func(t *testing.T) {
		et.Extract(t, xvideos.NewXVideos(), "http://xvideos.com/video.ucuvbkfda4e/a_beautiful_red-haired_stranger_was_refused_but_still_came_to_my_room_for_sex", et.Expect{
			ID: "ucuvbkfda4e", Title: "re:^A Beautiful Red-Haired Stranger", MinFormats: 3, MinDuration: 1200,
		})
	})
	t.Run("xnxx", func(t *testing.T) {
		et.Extract(t, xvideos.NewXNXX(), "http://www.xnxx.com/video-55awb78/skyrim_test_video", et.Expect{
			ID: "55awb78", Title: "re:(?i)skyrim test video", MinFormats: 3,
		})
	})
	t.Run("pornhub", func(t *testing.T) {
		et.Extract(t, aylo.NewPornhub(), "http://www.pornhub.com/view_video.php?viewkey=648719015", et.Expect{
			ID: "648719015", Title: "re:.+", MinFormats: 4, MinDuration: 360,
		})
	})
	t.Run("redtube", func(t *testing.T) {
		et.Extract(t, aylo.NewRedTube(), "https://www.redtube.com/38864951", et.Expect{ID: "38864951", Title: "re:.+", MinFormats: 3})
	})
}

func TestOnlineXHamster(t *testing.T) {
	et.Online(t)
	// ссылки на потоки обфусцированы — проверяем, что расшифровка дала рабочие форматы
	et.Extract(t, xhamster.New(), "https://xhamster.com/videos/femaleagent-shy-beauty-takes-the-bait-1509445", et.Expect{
		ID: "1509445", Title: "FemaleAgent Shy beauty takes the bait", MinFormats: 2, MinDuration: 800,
	})
}

func TestOnlineEporner(t *testing.T) {
	et.Online(t)
	// старые ролики удаляются — берём свежий с главной страницы
	page, err := ex.NewEnv().GetText(context.Background(), "https://www.eporner.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	path := regexp.MustCompile(`href="(/video-[A-Za-z0-9]+/[^"]*)"`).FindStringSubmatch(page)
	if path == nil {
		t.Fatal("no video link on eporner homepage")
	}
	et.Extract(t, eporner.New(), "https://www.eporner.com"+path[1], et.Expect{Title: "re:.+", MinFormats: 2})
}
