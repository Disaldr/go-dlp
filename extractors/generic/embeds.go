package generic

import "regexp"

// Распознавание встраиваний известных плееров прямо в HTML (не через
// <iframe>, который уже обрабатывается в fromHTML). Многие сайты просто
// вставляют Brightcove/Wistia/JWPlayer/Kaltura inline-разметкой; мы находим
// сигнатуру и строим канонический URL, который подхватывает соответствующий
// экстрактор. Один детектор даёт охват множества сайтов без отдельного
// экстрактора под каждый.

var (
	// Brightcove: <video-js data-account="123" data-player="x" data-video-id="456">.
	reBCAccount = regexp.MustCompile(`data-account\s*=\s*["']?(\d+)`)
	reBCVideo   = regexp.MustCompile(`data-video-id\s*=\s*["']?((?:ref:)?[\w-]+)`)
	reBCPlayer  = regexp.MustCompile(`data-player\s*=\s*["']?([A-Za-z0-9]+)`)

	// Wistia: class "wistia_async_{id}" или ссылка wistia.(net|com)/embed/(medias|iframe)/{id}.
	reWistia = regexp.MustCompile(`(?:wistia_async_|wistia\.(?:net|com)/(?:embed/)?(?:medias|iframe)/)([A-Za-z0-9]{10})`)

	// JWPlayer (облачный): cdn.jwplayer.com/players/{media}-{player} или
	// content.jwplatform.com/{players|videos|feeds|manifests}/{media}.
	reJW = regexp.MustCompile(`(?:cdn\.jwplayer\.com/players/|content\.jwplatform\.com/(?:players|videos|feeds|manifests)/)([A-Za-z0-9]{8})`)

	// Kaltura: entry id формата 0_xxxxxxxx / 1_xxxxxxxx + partner id рядом
	// (kWidget.embed({wid:"_123", entry_id:"0_abc"}) или /p/{pid}/... entry_id/{entry}).
	reKalturaEntry   = regexp.MustCompile(`["'/]entry_?[iI]d["'\s:=/]+([01]_[0-9a-zA-Z]{8})`)
	reKalturaPartner = regexp.MustCompile(`(?:partner_id["'\s:=]+|["']?wid["']?\s*:\s*["']_|/p/)(\d+)`)

	// Vimeo (inline, не iframe): data-vimeo-id + необязательный data-vimeo-hash,
	// либо явный player.vimeo.com/video/{id} в скрипте. Голые ссылки vimeo.com/{id}
	// не трогаем — это может быть просто ссылка, а не встраивание.
	reVimeoData   = regexp.MustCompile(`data-vimeo-id\s*=\s*["']?(\d+)`)
	reVimeoHash   = regexp.MustCompile(`data-vimeo-hash\s*=\s*["']?([\da-f]+)`)
	reVimeoPlayer = regexp.MustCompile(`player\.vimeo\.com/video/(\d+)(?:\?(?:[^"'\s]*&)?h=([\da-f]+))?`)

	// Dailymotion (inline): data-dailymotion-id или geo.dailymotion.com/player?video={id}.
	reDMData   = regexp.MustCompile(`data-dailymotion-id\s*=\s*["']?(x[0-9a-z]+)`)
	reDMPlayer = regexp.MustCompile(`geo\.dailymotion\.com/player[^"'\s]*[?&]video=(x[0-9a-z]+)`)
)

// detectInlineEmbeds возвращает канонические URL/схемы встроенных плееров,
// найденных в HTML-разметке страницы.
func detectInlineEmbeds(page string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}

	if acc := reBCAccount.FindStringSubmatch(page); acc != nil {
		if vid := reBCVideo.FindStringSubmatch(page); vid != nil {
			player := "default"
			if p := reBCPlayer.FindStringSubmatch(page); p != nil {
				player = p[1]
			}
			add("https://players.brightcove.net/" + acc[1] + "/" + player + "_default/index.html?videoId=" + vid[1])
		}
	}
	for _, m := range reWistia.FindAllStringSubmatch(page, -1) {
		add("https://fast.wistia.net/embed/iframe/" + m[1])
	}
	for _, m := range reJW.FindAllStringSubmatch(page, -1) {
		add("https://cdn.jwplayer.com/v2/media/" + m[1])
	}
	if entry := reKalturaEntry.FindStringSubmatch(page); entry != nil {
		if pid := reKalturaPartner.FindStringSubmatch(page); pid != nil {
			add("kaltura:" + pid[1] + ":" + entry[1])
		}
	}
	if v := reVimeoData.FindStringSubmatch(page); v != nil {
		u := "https://player.vimeo.com/video/" + v[1]
		if h := reVimeoHash.FindStringSubmatch(page); h != nil {
			u += "?h=" + h[1]
		}
		add(u)
	}
	for _, m := range reVimeoPlayer.FindAllStringSubmatch(page, -1) {
		u := "https://player.vimeo.com/video/" + m[1]
		if m[2] != "" {
			u += "?h=" + m[2]
		}
		add(u)
	}
	for _, m := range reDMData.FindAllStringSubmatch(page, -1) {
		add("https://www.dailymotion.com/video/" + m[1])
	}
	for _, m := range reDMPlayer.FindAllStringSubmatch(page, -1) {
		add("https://www.dailymotion.com/video/" + m[1])
	}
	return out
}
