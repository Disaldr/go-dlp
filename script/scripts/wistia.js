// Wistia: embedded videos. Публичный embed-конфиг отдаётся JSON по
// fast.wistia.net/embed/medias/{hashed_id}.json → media.assets[] (mp4/hls).
const WISTIA_JSON = "https://fast.wistia.net/embed/medias/";
const WISTIA_REF = { Referer: "https://fast.wistia.net/" };

extractor({
  name: "wistia",
  description: "Wistia embedded videos",
  match: [
    String.raw`^https?://(?:\w+\.)?wistia\.(?:net|com)/(?:embed/)?(?:iframe|medias)/([a-z0-9]{10})`,
    String.raw`^wistia:([a-z0-9]{10})`,
  ],
  extract(url, m) {
    const id = m[1];
    const r = http.get(WISTIA_JSON + id + ".json", { headers: WISTIA_REF, allowErrors: true });
    if (r.status === 404) fail.unavailable("media not found", id);
    if (r.status >= 400) fail.unavailable("embed config HTTP " + r.status, id);
    const body = r.json();
    const data = body && body.media;
    if (!data) fail.unavailable("no media in embed config", id);

    const formats = [];
    const thumbnails = [];
    for (const a of data.assets || []) {
      const aurl = a.url;
      if (!aurl) continue;
      if (a.status !== undefined && a.status !== 2) continue;
      const atype = a.type || "";
      if (atype === "preview" || atype === "storyboard") continue;

      // Картинки-превью.
      if (atype === "still" || atype === "still_image") {
        thumbnails.push({ url: aurl.replace(/\.bin$/, ".jpg"), width: a.width, height: a.height });
        continue;
      }

      const extMatch = aurl.match(/\.([a-z0-9]+)(?:[?#]|$)/i);
      const aext = a.ext || (extMatch ? extMatch[1] : undefined);
      let formatId = atype;
      if (/_video$/.test(atype) && a.display_name) formatId = atype.slice(0, -6) + "-" + a.display_name;

      // HLS-ассет: отдаём через hls().
      if (a.container === "m3u8" || aext === "m3u8") {
        for (const f of hls(aurl.replace(/\.bin$/, ".m3u8"), { id: "hls-" + formatId, headers: WISTIA_REF })) {
          formats.push(f);
        }
        continue;
      }

      const isAudio = a.display_name === "Audio" || /audio/i.test(atype);
      formats.push({
        format_id: formatId,
        url: aurl,
        ext: isAudio ? aext : (aext === "mov" ? "mp4" : aext) || "mp4",
        width: isAudio ? undefined : a.width,
        height: isAudio ? undefined : a.height,
        tbr: a.bitrate || undefined,
        vcodec: isAudio ? "none" : a.codec || undefined,
        filesize: a.size ? Number(a.size) : undefined,
        quality: atype === "original" ? 1 : undefined,
        http_headers: WISTIA_REF,
      });
    }
    if (!formats.length) fail.noFormats("no playable assets", id);

    return {
      id: data.hashedId || id,
      title: data.name || id,
      description: data.seoDescription,
      duration: data.duration ? Number(data.duration) : undefined,
      timestamp: data.createdAt ? Number(data.createdAt) : undefined,
      thumbnails: thumbnails.length ? thumbnails : undefined,
      formats: formats,
    };
  },
});
