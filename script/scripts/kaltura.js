// Kaltura: встроенные плееры (MediaSpace и др.). Нужны partner_id (pid) и entry_id.
// Формат-потоки берём из playManifest (HLS), метаданные — из публичного multirequest API.
const KAL_BASE = "https://cdnapisec.kaltura.com";
const KAL_API = KAL_BASE + "/api_v3/service/multirequest";

function parseIds(url, m) {
  const kal = url.match(/^kaltura:(\d+):([\w-]+)/);
  if (kal) return { pid: kal[1], entry: kal[2] };
  let pid = null, entry = null;
  let mm;
  if ((mm = url.match(/\/p\/(\d+)\//)) || (mm = url.match(/[?&/]partner_id[=/](\d+)/i)) ||
      (mm = url.match(/[?&/]wid[=/]_?(\d+)/i)) || (mm = url.match(/[?&/]p[=/](\d+)/i))) pid = mm[1];
  if ((mm = url.match(/[?&/]entry_?id[=/]([\w-]+)/i)) || (mm = url.match(/\/entryId\/([\w-]+)/i))) entry = mm[1];
  return { pid: pid, entry: entry };
}

function apiException(obj) {
  return obj && typeof obj === "object" && obj.objectType === "KalturaAPIException";
}

function fetchEntry(pid, entry) {
  const body = {
    apiVersion: "3.3.0",
    format: 1, // JSON
    ks: "",
    "1": { service: "session", action: "startWidgetSession", widgetId: "_" + pid },
    "2": {
      service: "baseEntry",
      action: "list",
      ks: "{1:result:ks}",
      filter: { redirectFromEntryId: entry },
      responseProfile: {
        type: 1,
        fields: "id,name,description,thumbnailUrl,dataUrl,duration,msDuration,createdAt,userId,plays,mediaType",
      },
    },
  };
  const r = http.post(KAL_API, { json: body, allowErrors: true });
  let data = null;
  try { data = r.json(); } catch (e) {}
  if (!Array.isArray(data)) {
    if (r.status >= 400) fail.unavailable("Kaltura API HTTP " + r.status, entry);
    return null;
  }
  for (const item of data) {
    if (apiException(item)) {
      const msg = item.message || item.code || "API exception";
      if (/entry_id|not.*found|invalid/i.test(msg)) fail.unavailable("Kaltura: " + msg, entry);
      fail.unavailable("Kaltura: " + msg, entry);
    }
  }
  const list = data[1];
  if (list && Array.isArray(list.objects) && list.objects.length) return list.objects[0];
  return null;
}

extractor({
  name: "kaltura",
  description: "Kaltura embed players",
  match: [
    String.raw`^kaltura:(\d+):([\w-]+)`,
    String.raw`^https?://(?:[a-z0-9-]+\.)?kaltura\.com/(?:p/\d+/|(?:index\.php/)?(?:kwidget|extwidget|html5)|html5/)`,
    String.raw`^https?://cdnapi(?:sec)?\.kaltura\.com/`,
  ],
  extract(url, m) {
    const ids = parseIds(url, m);
    if (!ids.pid || !ids.entry) fail.unsupported("could not find Kaltura partner_id / entry_id in URL");
    const pid = ids.pid, entry = ids.entry;

    const entryData = fetchEntry(pid, entry);
    const realEntry = (entryData && entryData.id) || entry;

    // playManifest → HLS. hls() сам сходит по ссылке и разберёт мастер-плейлист.
    const manifest = KAL_BASE + "/p/" + pid + "/sp/" + pid + "00/playManifest/entryId/" +
      realEntry + "/format/applehttp/protocol/https/a.m3u8";
    const headers = { Referer: "https://cdnapisec.kaltura.com/" };
    let formats = [];
    try {
      formats = hls(manifest, { id: "hls", headers: headers });
    } catch (e) {
      log.warn("kaltura playManifest hls: " + e);
    }

    if (!formats.length) {
      // Фоллбэк: единый progressive-поток через format/url.
      const prog = KAL_BASE + "/p/" + pid + "/sp/" + pid + "00/playManifest/entryId/" +
        realEntry + "/format/url/protocol/https/a.mp4";
      const head = http.request({ url: prog, method: "HEAD", headers: headers, allowErrors: true });
      if (head.status < 400 && head.url) {
        formats = [{ format_id: "source", url: head.url, ext: "mp4", http_headers: headers }];
      }
    }
    if (!formats.length) fail.noFormats("no playable Kaltura formats (entry may be private or geo-blocked)", realEntry);

    let duration;
    if (entryData) {
      duration = entryData.duration || (entryData.msDuration ? entryData.msDuration / 1000 : undefined);
    }

    return {
      id: realEntry,
      title: (entryData && entryData.name) || ("Kaltura " + realEntry),
      description: entryData && entryData.description,
      thumbnail: entryData && entryData.thumbnailUrl,
      duration: duration,
      timestamp: entryData && entryData.createdAt ? Number(entryData.createdAt) : undefined,
      uploader_id: entryData && entryData.userId,
      view_count: entryData && entryData.plays,
      formats: formats,
    };
  },
});
