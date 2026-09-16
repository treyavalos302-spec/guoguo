const fs = require("fs");
const assert = require("assert/strict");

const target = process.argv[2] || "./widgets/guoguo.js";
const source = fs.readFileSync(target, "utf8");
assert.match(source, /^WidgetMetadata\s*=/, "metadata must be the first statement and a bare assignment");

const calls = [];
const storage = {};
global.Widget = {
  http: {
    get: async (url, options = {}) => {
      calls.push({ url, options });
      assert.equal(options.headers.Authorization, "Bearer fixture-token");
      if (url.endsWith("/v1/list")) {
        return { data: { data: [{ id: "hongguo:1", title: "列表剧", posterUrl: "https://juku.test/image?sig=opaque", sourceUrl: "must-not-surface" }], hasMore: false } };
      }
      if (url.endsWith("/search")) {
        return { data: { data: [{ id: "hongguo:2", title: "搜索剧" }], hasMore: false } };
      }
      if (url.endsWith("/detail")) {
        const page = Number(options.params.page);
        if (page === 1) return { data: { id: "hongguo:1", title: "详情剧", backdropUrl: "https://juku.test/image?sig=opaque", episodes: [{ id: "ep-1", title: "第一集", videoUrl: "https://juku.test/stream.m3u8?sig=opaque" }], hasMore: true } };
        return { data: { id: "hongguo:1", title: "详情剧", episodes: [{ id: "ep-2", title: "第二集", videoUrl: "https://juku.test/stream.m3u8?sig=opaque2" }], hasMore: false } };
      }
      throw new Error("unmocked URL: " + url);
    }
  },
  storage: {
    get: key => storage[key],
    set: (key, value) => { storage[key] = value; }
  }
};
global.WidgetMetadata = {};

eval(source);

(async () => {
  assert.equal(WidgetMetadata.globalParams[0].name, "server");
  assert.equal(WidgetMetadata.globalParams[1].name, "token");
  assert.equal(WidgetMetadata.modules[0].functionName, "loadList");
  assert.equal(WidgetMetadata.search.functionName, "search");
  assert.deepEqual(WidgetMetadata.search.params.map(item => item.name), ["keyword", "page"]);

  const params = { server: "https://juku.test/private", token: "fixture-token", page: 2, count: 12 };
  const list = await loadList(params);
  assert.equal(list.length, 1);
  assert.equal(list[0].type, "url");
  assert.equal(list[0].link, "drama:hongguo:1");
  assert.equal(list[0].sourceUrl, undefined);
  assert.equal(calls[0].options.params.page, 2);
  assert.equal(calls[0].options.params.pageSize, 12);

  const found = await search({ keyword: "搜索", page: 3 });
  assert.equal(found[0].title, "搜索剧");
  const searchCall = calls.find(call => call.url.endsWith("/search"));
  assert.equal(searchCall.options.params.keyword, "搜索");
  assert.equal(searchCall.options.params.page, 3);

  const detail = await loadDetail("drama:hongguo:1");
  assert.equal(detail.type, "url");
  assert.equal(detail.link, "drama:hongguo:1");
  assert.ok(Array.isArray(detail.backdropPaths));
  assert.equal(detail.stills, undefined);
  assert.equal(detail.episodeItems.length, 2);
  assert.equal(detail.episodeItems[0].playerType, "system");
  assert.equal(detail.episodeItems[0].videoUrl, "https://juku.test/stream.m3u8?sig=opaque");
  assert.equal(detail.episodes, undefined);
  assert.equal(await loadDetail("invalid"), null);
  assert.deepEqual(storage["juku.forward.settings"], { server: "https://juku.test/private", token: "fixture-token" });
  assert.equal(calls.filter(call => call.url.endsWith("/detail")).length, 2);

  console.log("forward widget offline test: ok");
})().catch(error => {
  console.error(error);
  process.exit(1);
});
