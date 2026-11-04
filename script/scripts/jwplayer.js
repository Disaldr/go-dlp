// JWPlayer / JW Platform: контент-API отдаёт JSON по
// cdn.jwplayer.com/v2/media/{media_id} → playlist[0].sources[] (mp4 + m3u8).
const JW_API = "https://cdn.jwplayer.com/v2/media/";

extractor({
  name: "jwplayer",
  description: "JW Platform / JWPlayer videos",
  match: [
    String.raw`^https?://(?:content\.jwplatform|cdn\.jwplayer)\.com/(?:v2/media|players|videos|manifests|feeds)/([a-zA-Z0-9]{8})`,
    String.raw`^jwplatform:([a-zA-Z0-9]{8})`,
  ],
  extract(url, m) {
    const id = m[1];
    const r = http.get(JW_API + id, { allowErrors: true });
    if (r.status === 404) fail.unavailable("media not found", id);
    if (r.status >= 400) fail.unavailable("content API HTTP " + r.status, id);
    const data = r.json();
    const item = data && data.playlist && data.playlist[0];
    if (!item) fail.unavailable((data && data.message) || "no playlist item", id);

    const formats = [];
    for (const s of item.sources || []) {
      const su = s.file;
      if (!su) continue;
      const stype = s.type || "";
      if (stype === "application/vnd.apple.mpegurl" || /\.m3u8(?:[?#]|$)/.test(su)) {
        for (const f of hls(su, { id: "hls" })) formats.push(f);
        continue;
      }
      if (/^rtmp/i.test(su)) continue; // rtmp не поддерживаем
      formats.push({
        format_id: s.label || undefined,
        url: su,
        width: s.width || undefined,
        height: s.height || undefined,
        tbr: s.bitrate ? s.bitrate / 1000 : undefined,
        vcodec: /^audio\//.test(stype) ? "none" : undefined,
      });
    }
    if (!formats.length) fail.noFormats("no playable sources", id);

    const subtitles = {};
    for (const t of item.tracks || []) {
      if (t.kind === "captions" && t.file) {
        (subtitles[t.label || "und"] = subtitles[t.label || "und"] || []).push({ url: t.file });
      }
    }

    return {
      id: item.mediaid || id,
      title: item.title || data.title || id,
      description: item.description || data.description,
      duration: item.duration || undefined,
      timestamp: item.pubdate || undefined,
      thumbnail: item.image || undefined,
      tags: item.tags ? String(item.tags).split(",").map(function (t) { return t.trim(); }).filter(Boolean) : undefined,
      subtitles: Object.keys(subtitles).length ? subtitles : undefined,
      formats: formats,
    };
  },
});
