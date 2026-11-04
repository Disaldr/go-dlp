// Package all собирает встроенные экстракторы.
package all

import (
	ex "github.com/Disaldr/go-dlp/extractor"
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
	"github.com/Disaldr/go-dlp/extractors/reddit"
	"github.com/Disaldr/go-dlp/extractors/rutube"
	"github.com/Disaldr/go-dlp/extractors/soundcloud"
	"github.com/Disaldr/go-dlp/extractors/streamable"
	"github.com/Disaldr/go-dlp/extractors/telegram"
	"github.com/Disaldr/go-dlp/extractors/twitch"
	"github.com/Disaldr/go-dlp/extractors/vimeo"
	"github.com/Disaldr/go-dlp/extractors/vk"
	"github.com/Disaldr/go-dlp/extractors/xhamster"
	"github.com/Disaldr/go-dlp/extractors/xvideos"
	"github.com/Disaldr/go-dlp/extractors/youtube"
)

// Extractors возвращает новые экземпляры встроенных экстракторов.
func Extractors() []ex.Extractor {
	return []ex.Extractor{
		youtube.NewTab(),
		youtube.NewSearch(),
		youtube.New(),
		vimeo.New(),
		dailymotion.New(),
		rutube.New(),
		rutube.NewPlaylist(),
		twitch.NewVOD(),
		twitch.NewClip(),
		twitch.NewStream(),
		twitch.NewVideos(),
		soundcloud.New(),
		soundcloud.NewSet(),
		soundcloud.NewUser(),
		streamable.New(),
		reddit.New(),
		vk.New(),
		vk.NewVideos(),
		kick.NewLive(),
		kick.NewVOD(),
		kick.NewClip(),
		kick.NewVideos(),
		telegram.New(),
		xvideos.NewXVideos(),
		xvideos.NewXNXX(),
		aylo.NewPornhub(),
		aylo.NewRedTube(),
		aylo.NewYouPorn(),
		xhamster.New(),
		eporner.New(),
		okru.New(),
		coub.New(),
		bluesky.New(),
		bandcamp.NewTrack(),
		bandcamp.NewAlbum(),
		archiveorg.New(),
		peertube.New(),
		generic.New(),
	}
}

// Registry создаёт реестр со встроенными экстракторами.
func Registry() *ex.Registry {
	return ex.NewRegistry(Extractors()...)
}
