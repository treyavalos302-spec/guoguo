const assert = require('node:assert/strict');
const test = require('node:test');
const capabilities = require('./player-capabilities.js');
const type = 'video/mp4; codecs="avc1.42C01F, mp4a.40.2"';
function mse(supported) {class MediaSource {} MediaSource.isTypeSupported = () => supported; return {MediaSource};}
const native = {canPlayType: () => 'probably'}, absent = {canPlayType: () => ''};
test('Apple platforms prefer native HLS only when the video element reports support', () => {
  for (const agent of [{userAgent:'iPhone Safari'}, {userAgent:'iPad CriOS'}, {platform:'MacIntel',maxTouchPoints:5}, {userAgent:'Mozilla/5.0 (Macintosh; Intel Mac OS X)',platform:'MacIntel'}]) {
    assert.equal(capabilities(mse(true), native, agent).choose(type), 'hls');
    assert.equal(capabilities({}, native, agent).choose(type), 'hls');
    assert.equal(capabilities({}, absent, agent).choose(type), '');
    assert.equal(capabilities(mse(true), absent, agent).choose(type), 'mse');
  }
});
test('desktop MSE, native-only, and unsupported browsers are distinguished', () => {
  assert.equal(capabilities(mse(true), native, {}).choose(type),'mse');
  assert.equal(capabilities(mse(false), native, {}).choose(type),'hls');
  assert.equal(capabilities({}, absent, {}).choose(type),'');
  assert.equal(capabilities(mse(true), absent, {platform:'MacIntel',maxTouchPoints:0}).choose(type),'mse');
});
test('broken capability probes cannot invent or prevent native HLS support', () => {
  const environment=mse(true);environment.MediaSource.isTypeSupported=()=>{throw new Error('probe failed');};
  assert.equal(capabilities(environment,native,{}).choose(type),'hls');
  assert.equal(capabilities({MediaSource:{}},native,{}).choose(type),'hls');
  assert.equal(capabilities({}, {canPlayType:()=> 'no'}, {}).choose(type),'');
  assert.equal(capabilities({}, {canPlayType:()=> {throw new Error('probe failed');}}, {userAgent:'iPhone Safari'}).choose(type),'');
  assert.equal(capabilities({}, {}, {platform:'MacIntel'}).native(), false);
});

test('HLS recovery permits one rebuild per episode and manual reset', () => {
  const recovery = capabilities({}, native, {}).recovery();
  recovery.start(1);
  assert.equal(recovery.retry(1), true);
  assert.equal(recovery.retry(1), false);
  assert.equal(recovery.retry(2), true);
  assert.equal(recovery.retry(2), false);
  recovery.reset();
  assert.equal(recovery.retry(2), true);
});

test('remux requires compatible H.264 profiles and never replaces iOS HLS', () => {
  assert.equal(capabilities(mse(true), native, {}).remux(), true);
  assert.equal(capabilities(mse(false), native, {}).remux(), false);
  assert.equal(capabilities(mse(true), native, {userAgent:'iPhone Safari'}).remux(), false);
  assert.equal(capabilities(mse(true), absent, {platform:'MacIntel'}).remux(), true);
  const limited = mse(true);
  limited.MediaSource.isTypeSupported = type => type.includes('42E0');
  assert.equal(capabilities(limited, absent, {}).remux(), false);
  limited.MediaSource.isTypeSupported = () => {throw new Error('probe unavailable');};
  assert.equal(capabilities(limited, native, {}).remux(), false);
});
