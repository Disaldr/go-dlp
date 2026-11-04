// X / Twitter: посты с видео и GIF через syndication API (без входа).
extractor({
  name: "twitter",
  description: "X (Twitter) posts with video or GIF",
  match: [
    String.raw`^https?://(?:(?:www|mobile)\.)?(?:twitter|x)\.com/(?:[^/?#]+|i/web)/status/(\d+)`,
    String.raw`^https?://(?:www\.)?(?:fx|vx|fixup)(?:twitter|x)\.com/[^/?#]+/status/(\d+)`,
  ],
  extract(url, m) {
    const id = m[1];
    // Токен, который ожидает syndication API (тот же расчёт, что в виджете X).
    const token = ((Number(id) / 1e15) * Math.PI).toString(36).replace(/(0+|\.)/g, "");
    const r = http.get("https://cdn.syndication.twimg.com/tweet-result", {
      query: { id: id, token: token, lang: "en" },
      allowErrors: true,
    });
    if (r.status === 404) fail.unavailable("post not found or not public", id);
    if (r.status >= 400) fail.unavailable("syndication API: HTTP " + r.status, id);
    const t = r.json();
    if (!t || t.__typename === "TweetTombstone") {
      fail.unavailable((t && t.tombstone && t.tombstone.text && t.tombstone.text.text) || "post is unavailable", id);
    }

    const user = t.user || {};
    const text = (t.text || "").replace(/\s*https:\/\/t\.co\/\w+\s*$/, "");
    const base = {
      title: (user.name ? user.name + " - " : "") + (text.split("\n")[0].slice(0, 80) || "post " + id),
      description: text,
      uploader: user.name,
      uploader_id: user.screen_name,
      uploader_url: user.screen_name ? "https://x.com/" + user.screen_name : undefined,
      timestamp: t.created_at ? Math.floor(Date.parse(t.created_at) / 1000) : undefined,
      like_count: t.favorite_count,
      webpage_url: "https://x.com/" + (user.screen_name || "i") + "/status/" + id,
    };

    const videos = (t.mediaDetails || []).filter((md) => md.type === "video" || md.type === "animated_gif");
    if (videos.length === 0) fail.noFormats("this post has no video", id);

    const entries = videos.map((md, i) => {
      const formats = [];
      for (const v of (md.video_info && md.video_info.variants) || []) {
        if (v.content_type === "application/x-mpegURL") {
          try {
            for (const f of hls(v.url, { id: "hls" })) formats.push(f);
          } catch (e) {
            log.debug("hls failed", String(e));
          }
          continue;
        }
        const size = /\/(\d+)x(\d+)\//.exec(v.url);
        formats.push({
          format_id: "http-" + Math.round((v.bitrate || 0) / 1000),
          url: v.url,
          ext: "mp4",
          tbr: v.bitrate ? v.bitrate / 1000 : undefined,
          width: size ? Number(size[1]) : undefined,
          height: size ? Number(size[2]) : undefined,
          vcodec: md.type === "animated_gif" ? "avc1" : undefined,
          acodec: md.type === "animated_gif" ? "none" : undefined,
        });
      }
      const ms = md.video_info && md.video_info.duration_millis;
      return Object.assign({}, base, {
        id: videos.length > 1 ? id + "-" + (i + 1) : id,
        title: videos.length > 1 ? base.title + " #" + (i + 1) : base.title,
        thumbnail: md.media_url_https,
        duration: ms ? ms / 1000 : undefined,
        formats: formats,
      });
    });
    if (entries.length === 1) return entries[0];
    return Object.assign({}, base, { _type: "playlist", id: id, entries: entries });
  },
});
