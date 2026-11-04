package all_test

import (
	"testing"

	"github.com/Disaldr/go-dlp/extractor/extractortest"
	"github.com/Disaldr/go-dlp/extractors/all"
)

// TestURLRouting проверяет, что каждый URL попадает в правильный экстрактор
// (аналог only_matching-тестов yt-dlp). Пустой Want — только generic.
func TestURLRouting(t *testing.T) {
	extractortest.CheckMatches(t, all.Registry(), []extractortest.Match{
		// YouTube
		{"https://www.youtube.com/watch?v=jNQXAC9IVRw", "youtube"},
		{"https://youtu.be/jNQXAC9IVRw", "youtube"},
		{"https://www.youtube.com/shorts/jNQXAC9IVRw", "youtube"},
		{"https://www.youtube.com/watch?v=jNQXAC9IVRw&list=PLBCF2DAC6FFB574DE", "youtube"},
		{"https://www.youtube.com/playlist?list=PLBCF2DAC6FFB574DE", "youtube:tab"},
		{"https://www.youtube.com/@Google/videos", "youtube:tab"},
		{"https://www.youtube.com/channel/UCK8sQmJBp8GCxrOtXWBpyEA", "youtube:tab"},
		{"ytsearch5:golang", "youtube:search"},
		// Vimeo / Dailymotion
		{"https://vimeo.com/76979871", "vimeo"},
		{"https://player.vimeo.com/video/76979871?h=abcdef0123", "vimeo"},
		{"https://www.dailymotion.com/video/x5kesuj", "dailymotion"},
		{"https://dai.ly/x5kesuj", "dailymotion"},
		// Rutube
		{"https://rutube.ru/video/3eac3b4561676c17df9132a9a1e62e3e/", "rutube"},
		{"https://rutube.ru/play/embed/a10e53b86e8f349080f718582ce4c661", "rutube"},
		{"https://rutube.ru/video/embed/6722881", "rutube"},
		{"https://rutube.ru/channel/639184/videos/", "rutube:playlist"},
		{"https://rutube.ru/plst/308547/", "rutube:playlist"},
		// Twitch
		{"https://www.twitch.tv/videos/6528877", "twitch:vod"},
		{"https://www.twitch.tv/riotgames/v/6528877?t=5m10s", "twitch:vod"},
		{"https://player.twitch.tv/?video=v6528877", "twitch:vod"},
		{"https://clips.twitch.tv/FaintLightGullWholeWheat", "twitch:clips"},
		{"https://www.twitch.tv/rossbroadcast/clip/ConfidentBraveHumanChefFrank", "twitch:clips"},
		{"https://www.twitch.tv/shroomztv", "twitch:stream"},
		{"https://player.twitch.tv/?channel=shroomztv", "twitch:stream"},
		{"https://www.twitch.tv/directory", ""},
		{"https://www.twitch.tv/spamfish/videos", "twitch:videos"},
		{"https://www.twitch.tv/spamfish/videos?filter=archives&sort=views", "twitch:videos"},
		{"https://www.twitch.tv/spamfish/videos?filter=clips", ""},
		// SoundCloud
		{"https://soundcloud.com/ethmusic/lostin-powers-she-so-heavy", "soundcloud"},
		{"https://soundcloud.com/jaimemf/youtube-dl-test-video-a-y-baw/s-8Pjrp", "soundcloud"},
		{"https://api.soundcloud.com/tracks/123?secret_token=s-abc", "soundcloud"},
		{"https://soundcloud.com/the-concept-band/sets/the-royal-concept-ep", "soundcloud:set"},
		{"https://soundcloud.com/the-akashic-chronicler", "soundcloud:user"},
		{"https://soundcloud.com/the-akashic-chronicler/tracks", "soundcloud:user"},
		{"https://soundcloud.com/grynpyret/likes", "soundcloud:user"},
		{"https://soundcloud.com/discover", ""},
		// Streamable
		{"https://streamable.com/dnd1", "streamable"},
		{"https://streamable.com/e/dnd1", "streamable"},
		// Reddit
		{"https://www.reddit.com/r/videos/comments/6rrwyj/that_small_heart_attack/", "reddit"},
		{"https://old.reddit.com/r/videos/comments/6rrwyj/", "reddit"},
		{"https://www.reddit.com/user/test/comments/abc123/x", "reddit"},
		{"https://www.reddit.com/r/videos/", ""},
		// VK
		{"https://vk.com/videos-77521?z=video-77521_162222515%2Fclub77521", "vk"},
		{"https://vk.com/video205387401_165548505", "vk"},
		{"https://vkvideo.ru/video-77521_162222515", "vk"},
		{"https://vk.com/clip-220754053_456239150", "vk"},
		{"https://vk.com/video_ext.php?oid=-77521&id=162222515&hash=87b046504ccd8bfa", "vk"},
		{"https://m.vk.com/video-77521_162222515?list=ln-abc", "vk"},
		{"https://vk.com/id1", ""},
		// OK.ru
		{"https://ok.ru/video/20079905452", "odnoklassniki"},
		{"https://ok.ru/videoembed/20648036891", "odnoklassniki"},
		{"https://www.ok.ru/live/484531969818", "odnoklassniki"},
		{"https://ok.ru/dk?st.cmd=movieLayer&st.mvId=3054578486", "odnoklassniki"},
		{"https://ok.ru/group/123", ""},
		// Coub / Bluesky / Bandcamp / archive.org / PeerTube
		{"https://coub.com/view/5u5n1", "coub"},
		{"coub:5u5n1", "coub"},
		{"https://bsky.app/profile/bsky.app/post/3l3vgf77uco2g", "bluesky"},
		{"at://bsky.app/app.bsky.feed.post/3l3vgf77uco2g", "bluesky"},
		{"https://youtube-dl.bandcamp.com/track/youtube-dl-test-song", "bandcamp"},
		{"https://nightbringer.bandcamp.com/album/hierophany-of-the-open-grave", "bandcamp:album"},
		{"https://archive.org/details/XD300-23_68HighlightsAResearchCntAugHumanIntellect", "archive.org"},
		{"https://archive.org/details/Cops1922/Cops-v2.mp4", "archive.org"},
		{"https://framatube.org/w/kkGMgK9ZtnKfYAgnEtQxbv", "peertube"},
		{"https://peertube2.cpy.re/videos/watch/9c9de5e8-0a1e-484a-b099-e80766180a6d", "peertube"},
		{"https://videos.example.org/videos/embed/9c9de5e8-0a1e-484a-b099-e80766180a6d", "peertube"},
		// VK — списки
		{"https://vk.com/video/@mobidevices", "vk:uservideos"},
		{"https://vkvideo.ru/@mobidevices", "vk:uservideos"},
		{"https://vk.com/video/playlist/-174476437_2", "vk:uservideos"},
		{"https://vkvideo.ru/playlist/-51890028_-2", "vk:uservideos"},
		// Kick
		{"https://kick.com/xqc", "kick:live"},
		{"https://kick.com/xqc/videos", "kick:videos"},
		{"https://kick.com/xqc/videos/8f2c4b2a-1a2b-4c3d-9e8f-0a1b2c3d4e5f", "kick:vod"},
		{"https://kick.com/xqc/clips/clip_01HZ123ABC", "kick:clip"},
		{"https://kick.com/xqc?clip=clip_01HZ123ABC", "kick:clip"},
		{"https://kick.com/categories", ""},
		// Telegram
		{"https://t.me/europa_press/613", "telegram:embed"},
		{"https://t.me/s/europa_press/613", "telegram:embed"},
		{"https://t.me/europa_press", ""},
		// 18+
		{"https://www.xvideos.com/video65982001/what_s_her_name", "xvideos"},
		{"http://xvideos.com/video.ucuvbkfda4e/a_beautiful_red", "xvideos"},
		{"https://www.xvideos.com/embedframe/65982001", "xvideos"},
		{"https://www.xnxx.com/video-55awb78/skyrim_test_video", "xnxx"},
		{"https://www.pornhub.com/view_video.php?viewkey=648719015", "pornhub"},
		{"https://de.pornhub.com/view_video.php?viewkey=ph5e8b3c5b2e1a1", "pornhub"},
		{"https://www.pornhub.com/embed/ph5e8b3c5b2e1a1", "pornhub"},
		{"https://www.redtube.com/38864951", "redtube"},
		{"https://embed.redtube.com/?bgcolor=000000&id=1443286", "redtube"},
		{"https://www.youporn.com/watch/505835/sex-ed-is-it-safe-to-masturbate-daily/", "youporn"},
		{"https://www.youporn.com/embed/505835/sex-ed-is-it-safe-to-masturbate-daily/", "youporn"},
		{"https://xhamster.com/videos/femaleagent-shy-beauty-takes-the-bait-1509445", "xhamster"},
		{"http://xhamster.com/movies/2221348/britney_spears_sexy_booty.html?hd", "xhamster"},
		{"https://xhday.com/videos/strapless-threesome-xhh7yVf", "xhamster"},
		{"https://www.eporner.com/hd-porn/95008/Infamous-Tiffany-Teen-Strip-Tease-Video/", "eporner"},
		{"https://www.eporner.com/video-XfDqb9yLkfj/", "eporner"},
		{"https://www.eporner.com/embed/3YRUq1UCvue", "eporner"},
		// Прочее — только generic
		{"https://example.com/video.mp4", ""},
	})
}
