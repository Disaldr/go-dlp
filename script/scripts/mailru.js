// Mail.ru video: my.mail.ru/video/..., /mail/{user}/video/..., embeds and
// the +/video/meta/{id} API. yt-dlp resolves everything to the meta JSON
// https://my.mail.ru/+/video/meta/{id} → .videos[] (mp4 ladder) + .meta{...}.
// The meta response sets a `video_key` cookie that the CDN mp4 hosts require;
// the host shares one cookie jar, so we read it back and pass it on each format.
const MAILRU_HOST = "https://my.mail.ru";

extractor({
  name: "mailru",
  description: "Mail.ru video",
  match: [
    String.raw`^https?://(?:(?:www|m|videoapi)\.)?my\.mail\.ru/+(?:video/embed|\+/video/meta)/\d+`,
    String.raw`^https?://(?:(?:www|m|videoapi)\.)?my\.mail\.ru/+video/.*#video=`,
    String.raw`^https?://(?:(?:www|m|videoapi)\.)?my\.mail\.ru/+(?:videos/embed/)?(?:[^/]+/+){2}(?:video/(?:embed/)?)?[^/]+/\d+(?:\.html)?`,
  ],
  extract(url) {
    const path = url.replace(/^https?:\/\/(?:(?:www|m|videoapi)\.)?my\.mail\.ru\/+/, "");

    let metaId = null;
    let videoId = null;
    let mm;
    if ((mm = path.match(/^(?:video\/embed|\+\/video\/meta)\/(\d+)/))) {
      metaId = mm[1];
    } else if ((mm = url.match(/#video=\/?((?:[^/]+\/){3}\d+)/))) {
      videoId = mm[1];
    } else if ((mm = path.match(/^(?:videos\/embed\/)?((?:[^/]+\/+){2})(?:video\/(?:embed\/)?)?([^/]+\/\d+)(?:\.html)?/))) {
      videoId = mm[1].replace(/\/+/g, "/") + mm[2];
    }

    // Resolve the meta JSON URL.
    let metaUrl = null;
    if (metaId) {
      metaUrl = MAILRU_HOST + "/+/video/meta/" + metaId;
    } else {
      const page = http.get(url, { impersonate: "chrome" }).text;
      let cfg = null;
      let cm = page.match(/<script[^>]+class="sp-video__page-config"[^>]*>([\s\S]+?)<\/script>/);
      if (cm) { try { cfg = JSON.parse(cm[1]); } catch (e) { /* ignore */ } }
      if (!cfg) {
        cm = page.match(/"video"\s*:\s*(\{[\s\S]+?\}),/);
        if (cm) { try { cfg = JSON.parse(cm[1]); } catch (e) { /* ignore */ } }
      }
      if (cfg) {
        metaUrl = cfg.metaUrl || (cfg.video && cfg.video.metaUrl) || cfg.metadataUrl || null;
      }
    }

    let data = null;
    if (metaUrl) {
      if (/^\/\//.test(metaUrl)) metaUrl = "https:" + metaUrl;
      else if (/^\//.test(metaUrl)) metaUrl = urlJoin(MAILRU_HOST, metaUrl);
      const r = http.get(metaUrl, { headers: { Referer: url }, impersonate: "chrome", allowErrors: !videoId });
      if (r.status < 400) data = r.json();
    }

    // Fallback old endpoint (works only for the idv1/idv2 path form).
    if (!data && videoId) {
      data = http.get("https://api.video.mail.ru/videos/" + videoId + ".json?new=1", { impersonate: "chrome" }).json();
    }
    if (!data) fail.unavailable("could not resolve video meta", metaId || videoId);

    // The meta request drops a `video_key` cookie needed by the CDN hosts.
    const vkey = cookie(MAILRU_HOST, "video_key") || cookie(metaUrl || MAILRU_HOST, "video_key");
    const hdr = { Referer: url };
    if (vkey) hdr.Cookie = "video_key=" + vkey;

    const formats = [];
    for (const f of data.videos || []) {
      if (!f.url) continue;
      const fid = f.key ? String(f.key) : undefined;
      let height;
      const hmm = fid && fid.match(/^(\d+)[pP]$/);
      if (hmm) height = Number(hmm[1]);
      formats.push({
        url: urlJoin(metaUrl || url, f.url),
        format_id: fid,
        ext: "mp4",
        height: height,
        http_headers: hdr,
      });
    }
    if (!formats.length) fail.noFormats("no video formats in meta", metaId || videoId);

    const meta = data.meta || {};
    const author = data.author || {};
    const accId = meta.accId;
    const itemId = meta.itemId;
    const contentId = (accId && itemId) ? accId + "_" + itemId : (videoId || metaId);
    let title = meta.title || "";
    title = title.replace(/\.mp4$/, "");

    return {
      id: String(contentId),
      title: title,
      thumbnail: meta.poster,
      duration: meta.duration ? Number(meta.duration) : undefined,
      timestamp: meta.timestamp ? Number(meta.timestamp) : undefined,
      uploader: author.name,
      uploader_id: author.id || author.email,
      view_count: Number(data.viewsCount || data.views_count) || undefined,
      formats: formats,
    };
  },
});
