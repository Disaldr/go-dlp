// TikTok: видео по ссылке на пост и короткие ссылки vm./vt.tiktok.com.
extractor({
  name: "tiktok",
  description: "TikTok videos",
  match: [
    String.raw`^https?://(?:www\.|m\.)?tiktok\.com/(?:@[\w.-]*|embed(?:/v2)?|v)/(?:video/)?(\d+)`,
    String.raw`^https?://(?:www\.)?tiktok\.com/t/(\w+)`,
    String.raw`^https?://(?:vm|vt)\.tiktok\.com/(\w+)`,
  ],
  extract(url, m) {
    if (!/^\d+$/.test(m[1])) {
      // Короткая ссылка: TikTok редиректит на страницу поста.
      const r = http.get(url);
      if (r.url === url) fail.unsupported("short link did not redirect");
      return urlResult(r.url, "tiktok");
    }
    const id = m[1];
    const page = http.get("https://www.tiktok.com/@_/video/" + id, {
      headers: { Referer: "https://www.tiktok.com/" },
    }).text;
    const data = jsonAfter(page, '__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">');
    const scope = data && data.__DEFAULT_SCOPE__ && data.__DEFAULT_SCOPE__["webapp.video-detail"];
    if (!scope) fail.unavailable("no video data on the page (blocked or changed layout)", id);
    if (scope.statusCode === 10216 || scope.statusCode === 10222) fail.login("this video is private", id);
    if (scope.statusCode !== 0) fail.unavailable(scope.statusMsg || "video unavailable (" + scope.statusCode + ")", id);

    const it = scope.itemInfo.itemStruct;
    const v = it.video || {};
    const author = it.author || {};
    const headers = { Referer: "https://www.tiktok.com/" };
    const formats = [];
    for (const b of v.bitrateInfo || []) {
      const pa = b.PlayAddr || {};
      const urls = pa.UrlList || [];
      if (!urls.length) continue;
      formats.push({
        format_id: b.GearName,
        url: urls[0],
        ext: "mp4",
        width: pa.Width,
        height: pa.Height,
        tbr: b.Bitrate ? b.Bitrate / 1000 : undefined,
        vcodec: /265|hvc/.test(b.CodecType || "") ? "h265" : "h264",
        acodec: "aac",
        filesize: pa.DataSize ? Number(pa.DataSize) : undefined,
        http_headers: headers,
      });
    }
    if (!formats.length && v.playAddr) {
      formats.push({ format_id: "play", url: v.playAddr, ext: "mp4", width: v.width, height: v.height, http_headers: headers });
    }
    if (v.downloadAddr) {
      formats.push({ format_id: "download", format_note: "watermarked", url: v.downloadAddr, ext: "mp4", preference: -2, http_headers: headers });
    }
    if (!formats.length) fail.noFormats(it.imagePost ? "photo posts are not supported" : "no video formats", id);

    const stats = it.stats || {};
    return {
      id: id,
      title: (it.desc || "").split("\n")[0].slice(0, 100) || "TikTok video #" + id,
      description: it.desc,
      uploader: author.nickname,
      uploader_id: author.uniqueId,
      uploader_url: author.uniqueId ? "https://www.tiktok.com/@" + author.uniqueId : undefined,
      channel_id: author.secUid,
      timestamp: Number(it.createTime) || undefined,
      duration: v.duration,
      view_count: stats.playCount,
      like_count: stats.diggCount,
      thumbnail: v.cover || v.originCover,
      webpage_url: "https://www.tiktok.com/@" + (author.uniqueId || "_") + "/video/" + id,
      formats: formats,
    };
  },
});
