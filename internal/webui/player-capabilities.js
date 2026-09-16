(() => {
  function createPlaybackCapabilities(environment, video, agent) {
    const apple = /iPad|iPhone|iPod|Macintosh|Mac OS X/i.test(agent.userAgent || '') || /Mac/i.test(agent.platform || '');
    function native() {
      try {
        if (!video || typeof video.canPlayType !== 'function') return false;
        return ['application/vnd.apple.mpegurl', 'application/x-mpegURL'].some(type => ['maybe', 'probably'].includes(video.canPlayType(type)));
      } catch (_) {return false;}
    }
    function mse(type) {
      try {return typeof type === 'string' && Boolean(type) && typeof environment.MediaSource === 'function' && typeof environment.MediaSource.isTypeSupported === 'function' && environment.MediaSource.isTypeSupported(type);}
      catch (_) {return false;}
    }
    function choose(type) {
      const hls = native();
      if (apple && hls) return 'hls';
      if (mse(type)) return 'mse';
      return hls ? 'hls' : '';
    }
    function remux() {
      return !(apple && native()) && ['42E02A', '4D002A', '64002A'].every(profile => mse('video/mp4; codecs="avc1.' + profile + ', mp4a.40.2"'));
    }
    function recovery() {
      let episode = 0;
      let retries = 0;
      function start(index) {
        if (index !== episode) {
          episode = index;
          retries = 0;
        }
      }
      return {
        start,
        retry(index) {
          start(index);
          if (retries >= 1) return false;
          retries++;
          return true;
        },
        reset() {episode = 0; retries = 0;}
      };
    }
    return {native, mse, choose, remux, recovery};
  }
  if (typeof module !== 'undefined' && module.exports) module.exports = createPlaybackCapabilities;
  else window.JukuPlaybackCapabilities = createPlaybackCapabilities;
})();
