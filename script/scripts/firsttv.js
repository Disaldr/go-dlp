// 1tv.ru (Первый канал): страница содержит data-playlist-url, по которой
// отдаётся JSON-плейлист; у каждого элемента — sources[] (HLS/DASH/mp4).
// Отдельно поддержан прямой эфир 1tv.ru/live (DASH-манифест).

const REF = { Referer: "https://www.1tv.ru/" };

function srcExt(src, type) {
  if (type) {
    const t = type.toLowerCase();
    if (t.indexOf("mpegurl") >= 0) return "m3u8";
    if (t.indexOf("dash") >= 0) return "mpd";
    if (t === "video/mp4") return "mp4";
  }
  const m = /\.([a-z0-9]+)(?:[?#]|$)/i.exec(src);
  return m ? m[1].toLowerCase() : undefined;
}

function buildEntry(item) {
  const videoId = String(item.id || item.uid);
  const formats = [];
  const seen = {};

  for (const s of item.sources || []) {
    const src = s && s.src;
    if (!src || seen[src]) continue;
    seen[src] = 1;
    const ext = srcExt(src, s.type);
    if (ext === "m3u8") {
      for (const f of hls(src, { id: "hls", headers: REF })) formats.push(f);
    } else if (ext === "mpd") {
      for (const f of dash(src, { id: "dash" })) formats.push(f);
    } else {
      const tm = /_(\d{3,})\.[a-z0-9]+(?:[?#]|$)/i.exec(src);
      formats.push({
        url: src,
        ext: ext || "mp4",
        format_id: "http" + (tm ? "-" + tm[1] : ""),
        tbr: tm ? Number(tm[1]) : undefined,
        quality: -10,
        http_headers: REF,
      });
    }
  }

  // главы (эпизоды внутри выпуска)
  let chapters;
  if (Array.isArray(item.episodes)) {
    chapters = [];
    for (const e of item.episodes) {
      if (e && e.from != null) {
        chapters.push({
          start_time: Number(e.from),
          end_time: e.to != null ? Number(e.to) : undefined,
          title: e.name ? unescapeHTML(e.name) : undefined,
        });
      }
    }
    if (!chapters.length) chapters = undefined;
  }

  let uploadDate;
  if (item.date_air && /^\d{4}-\d{2}-\d{2}$/.test(item.date_air)) {
    uploadDate = item.date_air.replace(/-/g, "");
  }

  const res = {
    id: videoId,
    title: item.title ? unescapeHTML(item.title) : undefined,
    thumbnail: item.poster || item.poster_thumb || undefined,
    timestamp: item.dvr_begin_at != null ? Number(item.dvr_begin_at) : undefined,
    upload_date: uploadDate,
    duration: item.duration != null ? Number(item.duration) : undefined,
    age_limit: item.age_restriction != null ? Number(item.age_restriction) : undefined,
    chapters: chapters,
    formats: formats,
  };
  for (const k in res) if (res[k] === undefined) delete res[k];
  return res;
}

function parseQueryMulti(qs, keys) {
  const out = [];
  for (const pair of qs.split("&")) {
    const eq = pair.indexOf("=");
    if (eq < 0) continue;
    const k = decodeURIComponent(pair.slice(0, eq));
    const v = decodeURIComponent(pair.slice(eq + 1).replace(/\+/g, " "));
    if (keys.indexOf(k) >= 0 && v) out.push(v);
  }
  return out;
}

function meta(page, name) {
  let re = new RegExp('<meta[^>]+(?:property|name)=["\\\']' + name + '["\\\'][^>]*content=["\\\']([^"\\\']*)', "i");
  const mm = re.exec(page);
  return mm ? unescapeHTML(mm[1]) : undefined;
}

function liveExtract() {
  const r = http.get("https://stream.1tv.ru/api/playlist/1tvch-v1_as_array.json", { allowErrors: true });
  if (r.status >= 400) fail.unavailable("live playlist HTTP " + r.status, "live");
  const j = r.json();
  let mpdUrl;
  const mpd = j && j.mpd;
  if (Array.isArray(mpd)) {
    for (const u of mpd) if (u) { mpdUrl = u; break; }
  } else if (typeof mpd === "string") {
    mpdUrl = mpd;
  }
  if (!mpdUrl) fail.noFormats("no live MPD url", "live");
  return {
    id: "live",
    title: "Первый канал. Прямой эфир",
    is_live: true,
    formats: dash(mpdUrl, { id: "dash" }),
  };
}

extractor({
  name: "firsttv",
  description: "Первый канал (1tv.ru)",
  match: [
    String.raw`^https?://(?:www\.)?1tv\.ru/live`,
    String.raw`^https?://(?:www\.)?(?:sport)?1tv\.ru/(?:[^/?#]+/)+[^/?#]+`,
  ],
  extract(url) {
    if (/^https?:\/\/(?:www\.)?1tv\.ru\/live/.test(url)) {
      return liveExtract();
    }

    const r = http.get(url, { impersonate: "chrome", allowErrors: true });
    if (r.status >= 400) fail.unavailable("page HTTP " + r.status);
    const page = r.text;

    const pm = /data-playlist-url=["']([^"']+)/i.exec(page);
    if (!pm) fail.unavailable("no playlist url on page (layout changed or no video)");
    const playlistUrl = urlJoin(url, unescapeHTML(pm[1]));

    const qs = playlistUrl.indexOf("?") >= 0 ? playlistUrl.slice(playlistUrl.indexOf("?") + 1) : "";
    const itemIds = parseQueryMulti(qs, ["video_id", "videos_ids[]", "news_ids[]"]);

    const pr = http.get(playlistUrl, { impersonate: "chrome", headers: REF, allowErrors: true });
    if (pr.status >= 400) fail.unavailable("playlist HTTP " + pr.status);
    let data;
    try { data = pr.json(); } catch (e) { fail.unavailable("playlist is not JSON"); }
    if (!Array.isArray(data)) data = data ? [data] : [];

    const items = data.filter((v) =>
      v && v.uid != null && (itemIds.length ? itemIds.indexOf(String(v.uid)) >= 0 : true));
    if (!items.length) fail.unavailable("no playable items in playlist");

    const title = meta(page, "og:title");
    const thumbnail = meta(page, "og:image");

    if (items.length === 1) {
      const e = buildEntry(items[0]);
      if (!e.formats.length) fail.noFormats("no formats (content may be geo-restricted)", e.id);
      if (!e.title && title) e.title = title;
      return e;
    }

    const entries = items.map(buildEntry);
    return {
      _type: "playlist",
      id: itemIds.length ? itemIds[0] : (entries[0] && entries[0].id),
      title: title,
      thumbnail: thumbnail,
      entries: entries,
    };
  },
});
