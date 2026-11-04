// TED talks: https://www.ted.com/talks/{slug}. Данные о видео лежат в
// __NEXT_DATA__ (props.pageProps.videoData); медиа — HLS-манифест и mp4 (h264).
extractor({
  name: "ted",
  description: "TED talks",
  match: [
    String.raw`^https?://www\.ted\.com/talks(?:/lang/[^/#?]+)?/([\w-]+)`,
    String.raw`^https?://embed(?:-ssl)?\.ted\.com/talks(?:/lang/[^/#?]+)?/([\w-]+)`,
  ],
  extract(url, m) {
    const page = http.get("https://www.ted.com/talks/" + m[1]).text;
    const data = jsonAfter(page, '__NEXT_DATA__" type="application/json">');
    const talk = data && data.props && data.props.pageProps && data.props.pageProps.videoData;
    if (!talk) fail.unavailable("no video data on the page (blocked or changed layout)", m[1]);

    const id = String(talk.id || m[1]);
    const pd = talk.videoPlayerData || {};
    const res = pd.resources || {};
    const formats = [];

    if (res.hls && res.hls.stream) {
      for (const f of hls(res.hls.stream, { id: "hls" })) formats.push(f);
    }
    for (const r of res.h264 || []) {
      if (!r.file) continue;
      const br = r.bitrate ? Number(r.bitrate) : undefined;
      formats.push({
        url: r.file,
        format_id: br ? "h264-" + br + "k" : "h264",
        ext: "mp4",
        tbr: br,
        vcodec: "h264",
        acodec: "aac",
      });
    }
    if (talk.audioDownload) {
      formats.push({ url: talk.audioDownload, format_id: "audio", ext: "mp3", vcodec: "none" });
    }

    if (!formats.length) {
      const ext = pd.external || {};
      if (ext.service && ext.service.toLowerCase() === "youtube" && ext.code) {
        return urlResult("https://www.youtube.com/watch?v=" + ext.code);
      }
      if (ext.uri) return urlResult(ext.uri);
      fail.noFormats("no playable formats found", id);
    }

    let thumb = pd.thumb;
    if (thumb) thumb = thumb.split("?")[0];
    const tag = pd.targeting && pd.targeting.tag;

    return {
      id: id,
      title: talk.title,
      description: talk.description,
      uploader: talk.presenterDisplayName,
      thumbnail: thumb,
      duration: talk.duration ? Number(talk.duration) : undefined,
      view_count: talk.viewedCount ? Number(talk.viewedCount) : undefined,
      tags: tag ? tag.split(",") : undefined,
      webpage_url: "https://www.ted.com/talks/" + m[1],
      formats: formats,
    };
  },
});
