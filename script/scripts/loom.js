// Loom: запись по ссылке /share/{id} или /embed/{id}. Метаданные берём из
// GraphQL GetVideoSSR, ссылки на медиа — из /api/.../{raw-url,transcoded-url}
// (POST JSON → {url}) и CDN-URL из GraphQL GetVideoSource.
const LOOM_GRAPHQL = "https://www.loom.com/graphql";
const LOOM_APOLLO_VERSION = "45a5bd4";

const LOOM_QUERIES = {
  GetVideoSSR: `query GetVideoSSR($videoId: ID!, $password: String) {
  getVideo(id: $videoId, password: $password) {
    __typename
    ... on PrivateVideo { id status message __typename }
    ... on VideoPasswordMissingOrIncorrect { id message __typename }
    ... on RegularUserVideo {
      id __typename createdAt description name
      owner { display_name __typename }
      video_properties { duration height width __typename }
    }
  }
}`,
  GetVideoSource: `query GetVideoSource($videoId: ID!, $password: String, $acceptableMimes: [CloudfrontVideoAcceptableMime]) {
  getVideo(id: $videoId, password: $password) {
    ... on RegularUserVideo {
      id
      nullableRawCdnUrl(acceptableMimes: $acceptableMimes, password: $password) { url __typename }
      __typename
    }
    __typename
  }
}`,
};

function loomGraphql(op, videoId) {
  const variables = { videoId: videoId, password: null };
  if (op === "GetVideoSource") variables.acceptableMimes = ["DASH", "M3U8", "MP4", "WEBM"];
  const r = http.post(LOOM_GRAPHQL, {
    headers: {
      Accept: "application/json",
      "x-loom-request-source": "loom_web_" + LOOM_APOLLO_VERSION,
      "apollographql-client-name": "web",
      "apollographql-client-version": LOOM_APOLLO_VERSION,
      "graphql-operation-name": op,
      Origin: "https://www.loom.com",
    },
    json: { operationName: op, variables: variables, query: LOOM_QUERIES[op] },
    allowErrors: true,
  });
  return safeJson(r);
}

function safeJson(r) {
  if (!r || !r.text) return null;
  try {
    return r.json();
  } catch (e) {
    return null;
  }
}

// POST к /api/campaigns/sessions/{id}/{endpoint} → {url} (прямой mp4/webm,
// либо m3u8/mpd). Может вернуть пустой ответ — тогда формата нет.
function loomUrlApi(endpoint, videoId) {
  const r = http.post("https://www.loom.com/api/campaigns/sessions/" + videoId + "/" + endpoint, {
    headers: { Accept: "application/json" },
    json: { anonID: randomUUID(), deviceID: null, force_original: false, password: null },
    allowErrors: true,
  });
  const body = safeJson(r);
  return body && body.url ? body.url : null;
}

function randomUUID() {
  let s = "";
  for (let i = 0; i < 32; i++) {
    let v = Math.floor(Math.random() * 16);
    if (i === 12) v = 4;
    else if (i === 16) v = (v & 0x3) | 0x8;
    s += v.toString(16);
    if (i === 7 || i === 11 || i === 15 || i === 19) s += "-";
  }
  return s;
}

function stripQuery(u) {
  const i = u.indexOf("?");
  return i === -1 ? u : u.slice(0, i);
}

function loomFormats(videoId, props, formatUrl, formatId, quality) {
  if (!formatUrl) return [];
  const bare = stripQuery(formatUrl);
  if (/\.m3u8(?:$|[?#])/.test(bare)) {
    // Предсклеенные дорожки: -split.m3u8 → .m3u8.
    return hls(formatUrl.replace("-split.m3u8", ".m3u8"), { id: "hls-" + formatId });
  }
  if (/\.mpd(?:$|[?#])/.test(bare)) {
    return dash(formatUrl, { id: "dash-" + formatId });
  }
  const ext = (bare.match(/\.([a-z0-9]+)$/i) || [])[1];
  return [{
    format_id: "http-" + formatId,
    url: formatUrl,
    ext: ext ? ext.toLowerCase() : undefined,
    width: props.width,
    height: props.height,
    quality: quality,
  }];
}

extractor({
  name: "loom",
  description: "Loom screen recordings",
  match: [
    String.raw`^https?://(?:www\.)?loom\.com/(?:share|embed)/([\da-f]{32})`,
  ],
  extract(url, m) {
    const videoId = m[1];

    const ssr = loomGraphql("GetVideoSSR", videoId);
    const meta = (ssr && ssr.data && ssr.data.getVideo) || {};
    if (meta.__typename === "VideoPasswordMissingOrIncorrect") {
      fail.login("this video is password-protected", videoId);
    }
    if (meta.__typename === "PrivateVideo") {
      fail.login(meta.message || "this video is private", videoId);
    }
    const vp = meta.video_properties || {};

    const src = loomGraphql("GetVideoSource", videoId);
    const cdnUrl = src && src.data && src.data.getVideo &&
      src.data.getVideo.nullableRawCdnUrl && src.data.getVideo.nullableRawCdnUrl.url;

    const props = { width: vp.width || undefined, height: vp.height || undefined };
    let formats = [];
    const rawUrl = loomUrlApi("raw-url", videoId);
    formats = formats.concat(loomFormats(videoId, props, rawUrl, "raw", 1));
    const transcodedUrl = loomUrlApi("transcoded-url", videoId);
    formats = formats.concat(loomFormats(videoId, props, transcodedUrl, "transcoded", -1));

    // CDN-URL обычно дубликат raw/transcoded — берём, только если путь иной.
    if (cdnUrl) {
      const valid = [];
      if (rawUrl) valid.push(stripQuery(rawUrl));
      if (transcodedUrl) valid.push(stripQuery(transcodedUrl));
      if (valid.indexOf(stripQuery(cdnUrl)) === -1) {
        formats = formats.concat(loomFormats(videoId, props, cdnUrl, "cdn", 0));
      }
    }

    if (!formats.length) fail.noFormats("no playable streams (video may be unavailable)", videoId);

    return {
      id: videoId,
      title: meta.name,
      description: meta.description || undefined,
      uploader: meta.owner && meta.owner.display_name,
      timestamp: meta.createdAt ? Math.floor(Date.parse(meta.createdAt) / 1000) || undefined : undefined,
      duration: vp.duration || undefined,
      webpage_url: "https://www.loom.com/share/" + videoId,
      formats: formats,
    };
  },
});
