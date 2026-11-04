// Smotrim (smotrim.ru, VGTRK): видео, аудио, прямой эфир каналов и плейлисты.
// Медиа и метаданные берутся из плеерного API player-api.smotrim.ru/api/v1
// ({video|audio|channel}/<id> → streams.m3u8 / streams.mp3), плейлисты — из
// списочного API, с запасным разбором ссылок со страницы бренда.

const API = "https://player-api.smotrim.ru/api/v1";
const BASE = "https://smotrim.ru";

function apiGet(type, id) {
  const r = http.get(API + "/" + type + "/" + id, { impersonate: "chrome", allowErrors: true });
  if (r.status === 404) fail.unavailable(type + " " + id + " not found", id);
  if (r.status >= 400) fail.unavailable("player API HTTP " + r.status, id);
  let body;
  try { body = r.json(); } catch (e) { fail.unavailable("player API returned non-JSON", id); }
  if (!body || !body.data) {
    const msg = (body && body.notice) || "no player data";
    if (body && body.auth && body.auth.code) fail.login(msg, id);
    fail.unavailable(msg, id);
  }
  return body;
}

function splash(obj) {
  if (!obj) return undefined;
  return obj.large || obj.medium || obj.small || undefined;
}

function tsFromIso(s) {
  if (!s) return undefined;
  const t = Date.parse(s);
  return isNaN(t) ? undefined : Math.floor(t / 1000);
}

function buildMedia(type, body, urlId) {
  const d = body.data;
  const ep = d.episode || {};
  const brand = d.brand || {};
  const streams = d.streams || {};

  const isChannel = type === "channel";
  const formats = [];

  if (type === "audio") {
    if (!streams.mp3) notifyNoMedia(body, urlId);
    formats.push({
      format_id: "mp3",
      url: streams.mp3,
      ext: "mp3",
      vcodec: "none",
      http_headers: { Referer: BASE + "/" },
    });
  } else {
    if (!streams.m3u8) notifyNoMedia(body, urlId);
    for (const f of hls(streams.m3u8, { id: "hls", headers: { Referer: BASE + "/" } })) {
      formats.push(f);
    }
    if (!formats.length) fail.noFormats("no HLS formats (content may be geo-restricted)", urlId);
  }

  // субтитры
  let subtitles;
  if (Array.isArray(d.subtitles) && d.subtitles.length) {
    subtitles = {};
    for (const s of d.subtitles) {
      const url = s && (s.url || s.src);
      if (!url) continue;
      const lang = (s.lang || s.language || "ru").toLowerCase();
      (subtitles[lang] = subtitles[lang] || []).push({ url: url });
    }
  }

  const title = isChannel
    ? d.title
    : (ep.title || brand.title);

  const res = {
    id: String(d.publicId || urlId),
    title: title,
    description: ep.anons || ep.description || undefined,
    series: !isChannel ? brand.title : undefined,
    series_id: !isChannel && brand.id != null ? String(brand.id) : undefined,
    season: ep.season && (ep.season.title || (ep.season.number != null ? String(ep.season.number) : undefined)) || undefined,
    episode_number: ep.number != null ? Number(ep.number) : undefined,
    channel_id: isChannel && d.id != null ? String(d.id) : undefined,
    duration: d.duration ? Number(d.duration) : undefined,
    timestamp: tsFromIso(ep.airDate),
    thumbnail: splash(ep.splash) || splash(d.splash) || splash(brand.poster) || undefined,
    age_limit: d.ageRestriction != null ? Number(d.ageRestriction) : undefined,
    is_live: isChannel || undefined,
    webpage_url: d.shareLink || undefined,
    formats: formats,
    subtitles: subtitles,
  };
  for (const k in res) if (res[k] === undefined) delete res[k];
  return res;
}

function notifyNoMedia(body, id) {
  const notice = body.notice || (body.data && body.data.streams &&
    body.data.streams.downloadAvailability && body.data.streams.downloadAvailability.notice);
  if (body.auth && body.auth.code) fail.login(notice || "login required", id);
  if (notice && /регион|territ|geo|стран|доступ/i.test(notice)) fail.geo(notice, id);
  fail.noFormats(notice || "no playable stream (may require subscription or be geo-restricted)", id);
}

extractor({
  name: "smotrim",
  description: "Smotrim (VGTRK) video, audio and live channels",
  match: [
    String.raw`^(?:https?:)?//(?:(?:player|www)\.)?smotrim\.ru/(?:iframe/)?video(?:/id)?/\d+`,
    String.raw`^(?:https?:)?//(?:(?:player|www)\.)?smotrim\.ru/(?:iframe/)?audio(?:/id)?/\d+`,
    String.raw`^https?://(?:www\.)?smotrim\.ru/(?:brand|podcast)/\d+`,
    String.raw`^(?:https?:)?//(?:(?:player|www)\.)?smotrim\.ru/(?:iframe/)?channel(?:/id)?/\d+`,
    String.raw`^https?://(?:www\.)?smotrim\.ru/live/\d+`,
    String.raw`^https?://(?:www\.)?smotrim\.ru/article/\d+`,
  ],
  extract(url) {
    let m;

    if ((m = /smotrim\.ru\/(brand|podcast)\/(\d+)/i.exec(url))) {
      return playlist(m[1], m[2]);
    }

    if ((m = /smotrim\.ru\/(?:iframe\/)?video(?:\/id)?\/(\d+)/i.exec(url))) {
      return buildMedia("video", apiGet("video", m[1]), m[1]);
    }

    if ((m = /smotrim\.ru\/(?:iframe\/)?audio(?:\/id)?\/(\d+)/i.exec(url))) {
      return buildMedia("audio", apiGet("audio", m[1]), m[1]);
    }

    if ((m = /smotrim\.ru\/(?:iframe\/)?channel(?:\/id)?\/(\d+)/i.exec(url))) {
      const res = buildMedia("channel", apiGet("channel", m[1]), m[1]);
      res.display_id = m[1];
      return res;
    }

    // live с числовым id указывает на канал
    if ((m = /smotrim\.ru\/live\/(\d+)/i.exec(url))) {
      const res = buildMedia("channel", apiGet("channel", m[1]), m[1]);
      res.display_id = m[1];
      return res;
    }

    // Статья: ищем встроенное видео/аудио на странице (может грузиться скриптом)
    if ((m = /smotrim\.ru\/article\/(\d+)/i.exec(url))) {
      const r = http.get(url, { impersonate: "chrome", allowErrors: true });
      const page = r.status < 400 ? r.text : "";
      const vm = /\/video\/(\d+)/.exec(page);
      if (vm) return buildMedia("video", apiGet("video", vm[1]), vm[1]);
      const am = /\/audio\/(\d+)/.exec(page);
      if (am) return buildMedia("audio", apiGet("audio", am[1]), am[1]);
      fail.unavailable("no embedded media found in article (loaded dynamically)", m[1]);
    }

    fail.unsupported("unrecognized smotrim url");
  },
});

function meta(page, name) {
  let re = new RegExp('<meta[^>]+(?:property|name)=["\\\']' + name + '["\\\'][^>]*content=["\\\']([^"\\\']*)', "i");
  let mm = re.exec(page);
  if (mm) return unescapeHTML(mm[1]);
  re = new RegExp('<meta[^>]+content=["\\\']([^"\\\']*)["\\\'][^>]*(?:property|name)=["\\\']' + name + '["\\\']', "i");
  mm = re.exec(page);
  return mm ? unescapeHTML(mm[1]) : undefined;
}

function playlist(type, id) {
  const key = type === "podcast" ? "rubricId" : "brandId";
  const pr = http.get(BASE + "/" + type + "/" + id, { impersonate: "chrome", allowErrors: true });
  const page = pr.status < 400 ? pr.text : "";
  let title = page ? (meta(page, "og:title") || meta(page, "twitter:title")) : undefined;
  if (title) {
    title = title
      .replace(/смотреть онлайн[\s\S]*$/i, "")
      .replace(/[\s—,-]+$/, "")
      .replace(/[\s,—-]+(?:Передача|Сериал|Фильм|Программа|Подкаст)\s*$/i, "")
      .trim();
  }

  const seen = {};
  const entries = [];

  // Попытка через списочный API (полная пагинация)
  const endpoint = /\/audio\/\d+/.test(page) && !/\/video\/\d+/.test(page) ? "audios" : "videos";
  const limit = 15;
  for (let p = 1; p <= 60; p++) {
    const r = http.get(BASE + "/api/" + endpoint, {
      query: { [key]: id, limit: limit, page: p },
      headers: { Referer: BASE + "/", "X-Requested-With": "XMLHttpRequest" },
      impersonate: "chrome",
      allowErrors: true,
    });
    if (r.status >= 400) break;
    let j;
    try { j = r.json(); } catch (e) { break; }
    const contents = j && j.contents;
    const last = contents && contents.length ? contents[contents.length - 1] : null;
    const items = last && last.list;
    if (!items || !items.length) break;
    let added = 0;
    for (const it of items) {
      const link = it && it.link;
      if (link && !seen[link]) { seen[link] = 1; entries.push(urlResult(urlJoin(BASE, link))); added++; }
    }
    if (added === 0 || items.length < limit) break;
  }

  // Запасной вариант: ссылки со страницы (частичный список)
  if (!entries.length && page) {
    const re = /\/(?:video|audio)\/\d+/g;
    let mm;
    while ((mm = re.exec(page))) {
      const link = mm[0];
      if (!seen[link]) { seen[link] = 1; entries.push(urlResult(urlJoin(BASE, link))); }
    }
  }

  if (!entries.length) fail.unavailable("no items in playlist", id);
  return { _type: "playlist", id: String(id), title: title || undefined, entries: entries };
}
