// RT (rt.com / russian.rt.com и их текущие домены rt-english.com /
// russiatoday.ru). На странице материала видео лежит в og:video и/или прямой
// ссылкой на mp4 (mf.b37mrtl.ru). Прямой эфир /on-air/ отдаётся как HLS.

const HOSTS = String.raw`(?:www\.)?(?:rt\.com|rt-english\.com|russian\.rt\.com|russiatoday\.ru)`;

function og(page, prop) {
  let re = new RegExp('<meta[^>]+property=["\\\']og:' + prop + '["\\\'][^>]*content=["\\\']([^"\\\']*)', "i");
  let m = re.exec(page);
  if (m) return unescapeHTML(m[1]);
  re = new RegExp('<meta[^>]+content=["\\\']([^"\\\']*)["\\\'][^>]*property=["\\\']og:' + prop + '["\\\']', "i");
  m = re.exec(page);
  return m ? unescapeHTML(m[1]) : undefined;
}

function origin(u) {
  const m = /^(https?:\/\/[^/]+)/i.exec(u);
  return m ? m[1] : "https://www.rt.com";
}

function collectMedia(page) {
  const mp4 = [];
  const m3u8 = [];
  const seen = {};
  // mp4 на медиа-хостах RT
  let re = /https?:\/\/[a-z0-9.-]*(?:b37mrtl\.ru|cdnv\.rt\.com|vicdn[a-z0-9.-]*)\/[^"'\s)\\]+?\.mp4/gi;
  let m;
  while ((m = re.exec(page))) {
    const u = m[0];
    if (!seen[u]) { seen[u] = 1; mp4.push(u); }
  }
  // запасной вариант — любой .mp4
  if (!mp4.length) {
    re = /https?:\/\/[^"'\s)\\]+?\.mp4/gi;
    while ((m = re.exec(page))) {
      const u = m[0];
      if (!seen[u]) { seen[u] = 1; mp4.push(u); }
    }
  }
  re = /https?:\/\/[^"'\s)\\]+?\.m3u8[^"'\s)\\]*/gi;
  while ((m = re.exec(page))) {
    const u = m[0];
    if (!seen[u]) { seen[u] = 1; m3u8.push(u); }
  }
  return { mp4: mp4, m3u8: m3u8 };
}

function hexId(u) {
  const m = /([a-f0-9]{16,})\.mp4/i.exec(u);
  return m ? m[1] : undefined;
}

extractor({
  name: "rt",
  description: "RT (rt.com / russian.rt.com)",
  match: [
    String.raw`^https?://` + HOSTS + String.raw`/on-air`,
    String.raw`^https?://` + HOSTS + String.raw`/(?:[a-z0-9_-]+/)+\d{4,}-[\w-]+`,
  ],
  extract(url) {
    const r = http.get(url, { impersonate: "chrome", allowErrors: true });
    if (r.status >= 400) fail.unavailable("page HTTP " + r.status);
    const page = r.text;
    const ref = { Referer: origin(r.url || url) + "/" };

    // Прямой эфир
    if (/\/on-air(?:\/|$)/.test(url)) {
      const media = collectMedia(page);
      if (!media.m3u8.length) fail.noFormats("no live stream found", "on-air");
      return {
        id: "on-air",
        title: og(page, "title") || "RT Live",
        is_live: true,
        formats: hls(media.m3u8[0], { id: "hls", headers: ref }),
      };
    }

    const idm = /\/(\d{4,})-[\w-]+/.exec(url);
    const pageId = idm ? idm[1] : url;

    const title = og(page, "title");
    const description = og(page, "description");
    const thumbnail = og(page, "image");

    const media = collectMedia(page);
    const ogv = og(page, "video");

    // HLS имеет приоритет, если встретился
    if (media.m3u8.length) {
      return {
        id: String(pageId),
        title: title,
        description: description,
        thumbnail: thumbnail,
        formats: hls(media.m3u8[0], { id: "hls", headers: ref }),
      };
    }

    // упорядочиваем mp4: og:video первым
    const urls = [];
    const seen = {};
    if (ogv && /\.mp4/i.test(ogv)) {
      const clean = ogv.replace(/\.mp4.*$/i, ".mp4");
      urls.push(clean); seen[clean] = 1;
    }
    for (const u of media.mp4) if (!seen[u]) { seen[u] = 1; urls.push(u); }

    if (!urls.length) fail.noFormats("no video found on page", String(pageId));

    if (urls.length === 1) {
      return {
        id: String(pageId),
        title: title || String(pageId),
        description: description,
        thumbnail: thumbnail,
        formats: [{ url: urls[0], ext: "mp4", http_headers: ref }],
      };
    }

    const entries = urls.map((u) => {
      const hid = hexId(u) || u;
      return { id: hid, title: hid, formats: [{ url: u, ext: "mp4", http_headers: ref }] };
    });
    return {
      _type: "playlist",
      id: String(pageId),
      title: title,
      description: description,
      thumbnail: thumbnail,
      entries: entries,
    };
  },
});
