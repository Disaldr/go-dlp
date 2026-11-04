// Odysee / LBRY: видео по ссылке odysee.com. Публичный JSON-RPC proxy
// (resolve + get), медиа отдаётся player.odycdn.com с Referer odysee.com.
const ODYSEE_API = "https://api.na-backend.odysee.com/api/v1/proxy";
const ODYSEE_REF = { Referer: "https://odysee.com/" };

function odyseeCall(method, params) {
  const r = http.post(ODYSEE_API + "?m=" + method, {
    json: { jsonrpc: "2.0", method: method, params: params },
    allowErrors: true,
  });
  const body = r.json();
  if (body && body.error) fail.unavailable(body.error.message || "API error");
  if (r.status >= 400) fail.unavailable("API HTTP " + r.status);
  return body.result;
}

extractor({
  name: "odysee",
  description: "Odysee / LBRY videos",
  match: [
    // видео в канале: /@Channel:id/video-name:id
    String.raw`^https?://(?:odysee\.com|lbry\.tv|lbry\.lat)/(@[^/?#]+/[^/?#]+)`,
    // отдельное видео: /video-name:claimid
    String.raw`^https?://(?:odysee\.com|lbry\.tv|lbry\.lat)/([^@$/?#][^/?#]*[:#][0-9a-zA-Z]+)`,
  ],
  extract(url, m) {
    const path = decodeURIComponent(m[1]);
    const uri = "lbry://" + path.replace(/:/g, "#");

    const resolved = odyseeCall("resolve", { urls: [uri] });
    const claim = resolved && resolved[uri];
    if (!claim || claim.error) {
      fail.unavailable((claim && claim.error && claim.error.text) || "claim not found");
    }
    if (claim.value_type && claim.value_type !== "stream") {
      fail.unsupported("not a single video (" + claim.value_type + ")");
    }
    const v = claim.value || {};
    if (v.stream_type && v.stream_type !== "video" && v.stream_type !== "audio") {
      fail.noFormats("unsupported stream type: " + v.stream_type);
    }

    const streamingURL = odyseeCall("get", { uri: uri, save_file: false }).streaming_url;
    if (!streamingURL) fail.noFormats("no streaming url");

    // HEAD по streaming_url: следуем за редиректом и определяем HLS.
    let finalURL = streamingURL;
    const head = http.request({ url: streamingURL, method: "HEAD", headers: ODYSEE_REF, allowErrors: true });
    if (head.url) finalURL = head.url;

    let formats;
    if (/\.m3u8(?:[?#]|$)/.test(finalURL)) {
      formats = hls(finalURL, { id: "hls", headers: ODYSEE_REF });
    } else {
      const vid = v.video || v.audio || {};
      formats = [{
        format_id: "source",
        url: streamingURL,
        ext: v.source && mediaExt(v.source.media_type),
        width: vid.width || undefined,
        height: vid.height || undefined,
        filesize: v.source && v.source.size ? Number(v.source.size) : undefined,
        vcodec: v.stream_type === "audio" ? "none" : undefined,
        http_headers: ODYSEE_REF,
      }];
    }

    const ch = claim.signing_channel;
    return {
      id: claim.claim_id,
      title: v.title || claim.name,
      description: v.description,
      thumbnail: v.thumbnail && v.thumbnail.url,
      duration: (v.video && v.video.duration) || (v.audio && v.audio.duration),
      timestamp: v.release_time ? Number(v.release_time) : claim.timestamp,
      uploader: ch && ch.value && ch.value.title,
      uploader_id: ch && ch.name,
      channel_id: ch && ch.claim_id,
      channel_url: ch && ch.canonical_url ? "https://odysee.com/" + ch.canonical_url.replace(/^lbry:\/\//, "").replace(/#/g, ":") : undefined,
      tags: v.tags,
      license: v.license,
      formats: formats,
    };
  },
});

function mediaExt(mime) {
  if (!mime) return undefined;
  const map = { "video/mp4": "mp4", "video/webm": "webm", "video/quicktime": "mov",
                "audio/mpeg": "mp3", "audio/mp4": "m4a", "audio/ogg": "ogg", "audio/x-wav": "wav" };
  return map[mime];
}
