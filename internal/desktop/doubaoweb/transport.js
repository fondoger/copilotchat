async (operation, args) => {
  const origin = 'https://www.doubao.com';
  if (operation === 'ready') {
    return {ready: location.origin === origin && typeof window.bdms?.frontierSign === 'function'};
  }
  if (operation === 'abort') {
    window.__copilotDoubaoRequest?.controller.abort();
    window.__copilotDoubaoRequest = null;
    return;
  }
  if (location.origin !== origin) throw new Error('origin');

  switch (operation) {
    case 'sign': {
      const cookie = name => document.cookie.split(';').map(s => s.trim()).find(s => s.startsWith(name + '='))?.slice(name.length + 1) || '';
      const stored = key => {
        try { return JSON.parse(localStorage.getItem(key) || '{}'); } catch { return {}; }
      };
      const device = stored('samantha_web_web_id').web_id || '';
      const web = stored('__tea_cache_tokens_497858').web_id || '';
      const query = new URLSearchParams({
        aid: '497858', device_id: String(device), device_platform: 'web', doubao_device_platform: 'web',
        web_platform: 'browser', fp: cookie('s_v_web_id'), language: 'zh', pc_version: '3.36.0',
        doubao_pc_version: '3.36.0', pkg_type: 'release_version', real_aid: '497858', region: 'CN',
        sys_region: 'CN', samantha_web: '1', tea_uuid: String(web), tz_name: 'Asia/Shanghai',
        'use-olympus-account': '1', version_code: '20800', web_id: String(web), web_tab_id: args.tab
      });
      const token = cookie('msToken');
      if (token) query.set('msToken', token);
      query.sort();
      const signature = await window.bdms.frontierSign(query.toString());
      if (typeof signature === 'string' && signature) {
        query.set('X-Bogus', signature);
      } else if (signature && typeof signature === 'object') {
        let found = false;
        for (const key of ['a_bogus', 'X-Bogus']) {
          if (signature[key]) {
            query.set(key, signature[key]);
            found = true;
            break;
          }
        }
        if (!found) throw new Error('signature');
      } else {
        throw new Error('signature');
      }
      return {
        url: location.origin + args.path + '?' + query.toString(),
        ua: navigator.userAgent,
        csrf: cookie('passport_csrf_token') || cookie('passport_csrf_token_default')
      };
    }
    case 'fetch': {
      if (new URL(args.url).origin !== origin) throw new Error('origin');
      const controller = new AbortController();
      window.__copilotDoubaoRequest = {controller};
      const json = args.contentType.startsWith('application/json');
      const headers = {'Content-Type': args.contentType, 'agw-js-conv': 'str'};
      if (json) headers.Accept = 'application/json, text/plain, */*';
      if (args.csrf) headers['x-tt-passport-csrf-token'] = args.csrf;
      const bytes = Uint8Array.from(atob(args.body), c => c.charCodeAt(0));
      const body = json ? new TextDecoder().decode(bytes) : bytes;
      const response = await fetch(args.url, {
        method: 'POST', headers, body, credentials: 'include', redirect: 'error', signal: controller.signal
      });
      window.__copilotDoubaoRequest.reader = response.body.getReader();
      return {status: response.status, contentType: response.headers.get('content-type') || ''};
    }
    case 'read': {
      const state = window.__copilotDoubaoRequest;
      if (!state?.reader) throw new Error('reader');
      const {done, value} = await state.reader.read();
      let data = '';
      if (value) {
        for (let i = 0; i < value.length; i += 8192) {
          data += String.fromCharCode(...value.subarray(i, i + 8192));
        }
      }
      return {done, data: btoa(data)};
    }
    default:
      throw new Error('operation');
  }
}
