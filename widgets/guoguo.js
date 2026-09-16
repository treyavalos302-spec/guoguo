WidgetMetadata = {
  id: "com.guoguo.juku",
  title: "果果剧库",
  version: "1.0.0",
  requiredVersion: "0.0.1",
  description: "通过私密 Forward API 浏览和播放果果剧库",
  author: "果果剧库",
  detailCacheDuration: 60,
  globalParams: [
    { name: "server", title: "服务器", type: "input", value: "" },
    { name: "token", title: "访问令牌", type: "input", value: "" }
  ],
  modules: [{
    id: "loadList",
    title: "剧库",
    functionName: "loadList",
    cacheDuration: 60,
    requiresWebView: false,
    sectionMode: false,
    params: [
      { name: "page", title: "页码", type: "page" },
      { name: "count", title: "每页数量", type: "count", value: 24 }
    ]
  }],
  search: {
    title: "搜索",
    functionName: "search",
    params: [
      { name: "keyword", title: "关键词", type: "input" },
      { name: "page", title: "页码", type: "page" }
    ]
  }
};

const JUKU_DEFAULT_SERVER = "";
const JUKU_DEFAULT_TOKEN = "";

function jukuSettings(params = {}) {
  const saved = Widget.storage.get("juku.forward.settings") || {};
  const server = String(params.server || saved.server || JUKU_DEFAULT_SERVER || "").replace(/\/+$/, "");
  const token = String(params.token || saved.token || JUKU_DEFAULT_TOKEN || "");
  if (!server || !token) throw new Error("请配置果果剧库服务器和访问令牌");
  Widget.storage.set("juku.forward.settings", { server, token });
  return { server, token };
}

async function jukuGet(endpoint, params, settings) {
  const current = settings || jukuSettings(params || {});
  const response = await Widget.http.get(current.server + endpoint, {
    headers: { Authorization: "Bearer " + current.token, Accept: "application/json" },
    params: params || {}
  });
  if (!response || !response.data) throw new Error("果果剧库返回空响应");
  return response.data;
}

function jukuItem(item) {
  return {
    id: String(item.id),
    type: "url",
    title: item.title || "短剧",
    link: "drama:" + String(item.id),
    posterPath: item.posterUrl || "",
    backdropPath: item.backdropUrl || item.posterUrl || "",
    description: item.description || "",
    releaseDate: item.onlineDate || "",
    rating: Number(item.rating || 0)
  };
}

async function loadList(params = {}) {
  try {
    const settings = jukuSettings(params);
    const body = await jukuGet("/v1/list", {
      page: Math.max(1, Number(params.page || 1)),
      pageSize: Math.max(1, Math.min(100, Number(params.count || 24)))
    }, settings);
    return (body.data || []).map(jukuItem);
  } catch (error) {
    console.error("[loadList] 失败:", error.message || error);
    throw error;
  }
}

async function search(params = {}) {
  try {
    const settings = jukuSettings(params);
    const body = await jukuGet("/search", {
      keyword: String(params.keyword || ""),
      page: Math.max(1, Number(params.page || 1)),
      pageSize: 24
    }, settings);
    return (body.data || []).map(jukuItem);
  } catch (error) {
    console.error("[search] 失败:", error.message || error);
    throw error;
  }
}

async function loadDetail(link) {
  try {
    const text = String(link || "");
    if (!text.startsWith("drama:")) return null;
    const id = text.slice(6);
    if (!id) return null;
    const settings = jukuSettings({});
    let page = 1;
    let detail = null;
    const episodes = [];
    while (page <= 20) {
      const body = await jukuGet("/detail", { id, page, pageSize: 100 }, settings);
      if (!detail) detail = body;
      episodes.push(...(body.episodes || []));
      if (!body.hasMore) break;
      page += 1;
    }
    if (!detail) return null;
    return {
      id: String(detail.id),
      type: "url",
      title: detail.title || "短剧",
      link: "drama:" + String(detail.id),
      posterPath: detail.posterUrl || "",
      backdropPath: detail.backdropUrl || detail.posterUrl || "",
      backdropPaths: detail.backdropUrl ? [detail.backdropUrl] : [],
      description: detail.description || "",
      releaseDate: detail.onlineDate || "",
      rating: Number(detail.rating || 0),
      episodeItems: episodes.map((episode) => ({
        id: String(episode.id),
        type: "url",
        title: episode.title || "分集",
        videoUrl: episode.videoUrl,
        playerType: "system"
      }))
    };
  } catch (error) {
    console.error("[loadDetail] 失败:", error.message || error);
    throw error;
  }
}
