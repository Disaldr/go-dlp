package generic

import (
	"slices"
	"testing"
)

func TestDetectInlineEmbeds(t *testing.T) {
	cases := []struct {
		name string
		html string
		want []string
	}{
		{
			"brightcove video-js",
			`<video-js data-account="1507807800001" data-player="abCD1234" data-embed="default" data-video-id="6071787405001" class="vjs"></video-js>`,
			[]string{"https://players.brightcove.net/1507807800001/abCD1234_default/index.html?videoId=6071787405001"},
		},
		{
			"brightcove default player + ref id",
			`<video data-video-id="ref:my-ref" data-account="123456"></video>`,
			[]string{"https://players.brightcove.net/123456/default_default/index.html?videoId=ref:my-ref"},
		},
		{
			"wistia async class",
			`<div class="wistia_embed wistia_async_e4a27b971d seo=false"></div>`,
			[]string{"https://fast.wistia.net/embed/iframe/e4a27b971d"},
		},
		{
			"wistia script url",
			`<script src="https://fast.wistia.com/embed/medias/abc1234567.jsonp"></script>`,
			[]string{"https://fast.wistia.net/embed/iframe/abc1234567"},
		},
		{
			"jwplayer cloud",
			`<script src="https://cdn.jwplayer.com/players/nPripu9l-ABCdefgh.js"></script>`,
			[]string{"https://cdn.jwplayer.com/v2/media/nPripu9l"},
		},
		{
			"kaltura kWidget",
			`kWidget.embed({targetId:"x",wid:"_1770401",uiconf_id:37307382,flashvars:{},"entry_id":"0_58u8kme7"});`,
			[]string{"kaltura:1770401:0_58u8kme7"},
		},
		{
			"vimeo data attrs with hash",
			`<div data-vimeo-id="76979871" data-vimeo-hash="abc123def4"></div>`,
			[]string{"https://player.vimeo.com/video/76979871?h=abc123def4"},
		},
		{
			"vimeo player url in script",
			`<script>var u="https://player.vimeo.com/video/123456?h=deadbeef12&title=0";</script>`,
			[]string{"https://player.vimeo.com/video/123456?h=deadbeef12"},
		},
		{
			"dailymotion geo player",
			`<iframe src="https://geo.dailymotion.com/player/xabcd.html?video=x2jvvep&mute=true"></iframe>`,
			[]string{"https://www.dailymotion.com/video/x2jvvep"},
		},
		{
			"bare vimeo link ignored",
			`<a href="https://vimeo.com/76979871">watch on vimeo</a>`,
			nil,
		},
		{
			"no embed",
			`<p>just text, no players here</p>`,
			nil,
		},
	}
	for _, c := range cases {
		got := detectInlineEmbeds(c.html)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.name, got, c.want)
		}
	}
}
