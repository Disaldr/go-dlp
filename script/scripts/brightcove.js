// Brightcove: встроенный плеер players.brightcove.net/{account}/{player}_{embed}/index.html?videoId={id}.
// policyKey берём из config.json либо index.min.js плеера, затем Playback API.
const BC_PLAYERS = "https://players.brightcove.net";
const BC_EDGE = "https://edge.api.brightcove.com/playback/v1";

function policyKey(account, playerEmbed) {
  // 1) config.json → video_cloud.policy_key
  const cfg = http.get(BC_PLAYERS + "/" + account + "/" + playerEmbed + "/config.json", { allowErrors: true });
  if (cfg.status < 400) {
    const j = cfg.json();
    const pk = j && j.video_cloud && j.video_cloud.policy_key;
    if (pk) return pk;
  }
  // 2) index.min.js → policyKey:"..."
  const js = http.get(BC_PLAYERS + "/" + account + "/" + playerEmbed + "/index.min.js", { allowErrors: true });
  if (js.status < 400) {
    const m = js.text.match(/policyKey\s*:\s*(["'])(.+?)\1/);
    if (m) return m[2];
    const cat = js.text.match(/catalog\(\{[^}]*?policyKey\s*:\s*(["'])(.+?)\1/);
    if (cat) return cat[2];
  }
  return null;
}

function buildFormats(video, headers) {
  const formats = [];
  for (const s of video.sources || []) {
    const src = s.src || s.streaming_src;
    if (!src) continue; // RTMP (app_name/stream_name) — пропускаем
    const type = (s.type || "").toLowerCase();
    if (type.indexOf("mpegurl") >= 0 || /\.m3u8(?:[?#]|$)/.test(src) || s.container === "M2TS") {
      try { for (const f of hls(src, { id: "hls", headers: headers })) formats.push(f); } catch (e) { log.warn("brightcove hls: " + e); }
      continue;
    }
    if (type.indexOf("dash") >= 0 || /\.mpd(?:[?#]|$)/.test(src)) {
      try { for (const f of dash(src, { id: "dash" })) formats.push(f); } catch (e) { log.warn("brightcove dash: " + e); }
      continue;
    }
    const tbr = s.avg_bitrate ? s.avg_bitrate / 1000 : undefined;
    const w = s.width, h = s.height;
    const container = (s.container || "").toLowerCase();
    let fid = "http";
    if (tbr) fid += "-" + Math.round(tbr) + "k";
    if (h) fid += "-" + h + "p";
    formats.push({
      format_id: fid,
      url: src,
      ext: container || "mp4",
      width: w || undefined,
      height: h || undefined,
      tbr: tbr,
      filesize: s.size ? Number(s.size) : undefined,
      vcodec: (w === 0 && h === 0) ? "none" : (s.codec || undefined),
      http_headers: headers,
    });
  }
  return formats;
}

extractor({
  name: "brightcove",
  description: "Brightcove players (players.brightcove.net embeds)",
  match: [
    String.raw`^https?://players\.brightcove\.net/(\d+)/([^/]+)/index\.html`,
    String.raw`^brightcove:(\d+):([^/:]+):([\w:-]+)`, // brightcove:account:player_embed:videoId
  ],
  extract(url, m) {
    let account, playerEmbed, videoId, contentType = "video";
    if (url.indexOf("brightcove:") === 0) {
      account = m[1]; playerEmbed = m[2]; videoId = m[3];
    } else {
      account = m[1];
      playerEmbed = m[2];
      const vid = url.match(/[?&](?:videoId|videoID)=([^&#]+)/i);
      const pl = url.match(/[?&]playlistId=([^&#]+)/i);
      if (vid) videoId = decodeURIComponent(vid[1]);
      else if (pl) fail.unsupported("Brightcove playlists are not supported (playlistId=" + decodeURIComponent(pl[1]) + ")");
      else fail.unsupported("no videoId in embed URL");
    }

    const pk = policyKey(account, playerEmbed);
    if (!pk) fail.unavailable("could not extract Brightcove policyKey for " + account + "/" + playerEmbed);

    const api = BC_EDGE + "/accounts/" + account + "/videos/" + videoId;
    const resp = http.get(api, {
      headers: { Accept: "application/json;pk=" + pk },
      allowErrors: true,
    });

    if (resp.status >= 400) {
      let err = null;
      try { err = resp.json(); } catch (e) {}
      const e0 = Array.isArray(err) ? err[0] : err;
      const subcode = e0 && e0.error_subcode, code = e0 && e0.error_code;
      const msg = (e0 && (e0.message || e0.error_subcode || e0.error_code)) || ("HTTP " + resp.status);
      if (subcode === "CLIENT_GEO") fail.geo(msg, videoId);
      if (code === "ACCOUNT_AUTH_REQUIRED" || code === "VIDEO_NOT_PLAYABLE" || resp.status === 401) fail.login(msg, videoId);
      fail.unavailable(msg, videoId);
    }

    const video = resp.json();
    if (video.errors) {
      const e0 = video.errors[0] || {};
      fail.noFormats(e0.message || e0.error_subcode || e0.error_code || "no playable sources", videoId);
    }

    const headers = { Referer: "https://players.brightcove.net/", Origin: "https://players.brightcove.net" };
    const formats = buildFormats(video, headers);
    if (!formats.length) fail.noFormats("no supported sources", videoId);

    const dur = video.duration ? video.duration / 1000 : undefined;
    const poster = video.poster || video.thumbnail;
    const tags = Array.isArray(video.tags) ? video.tags.filter((t) => typeof t === "string") : undefined;

    return {
      id: String(video.id || videoId),
      title: video.name || ("Brightcove video " + videoId),
      description: video.description || video.long_description,
      thumbnail: poster,
      duration: dur && dur > 0 ? dur : undefined,
      is_live: dur !== undefined && dur <= 0 ? true : undefined,
      timestamp: video.published_at ? Math.floor(Date.parse(video.published_at) / 1000) || undefined : undefined,
      uploader_id: account,
      tags: tags,
      formats: formats,
    };
  },
});
