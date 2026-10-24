(() => {
  const config = __DOUBAO_CONFIG__;
  if (!config.chat || window !== window.top || location.origin !== 'https://' + config.host || window.__copilotDoubao) return;

  const hideSidebar = () => {
    const style = document.createElement('style');
    style.textContent = '#chat-route-main > :first-child, div[data-header-host="main"] { display: none !important; }';
    document.head.appendChild(style);
  };
  if (document.head) hideSidebar();
  else document.addEventListener('DOMContentLoaded', hideSidebar, {once: true});

  document.addEventListener('keydown', event => {
    if (!event.isTrusted || event.isComposing || event.repeat || event.altKey) return;
    const primary = /Mac/.test(navigator.platform) ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
    const key = event.key.toLowerCase();
    let command;
    if (event.ctrlKey && !event.metaKey && key === 'tab') command = event.shiftKey ? 'previous-chat' : 'next-chat';
    else if (event.ctrlKey && !event.metaKey && !event.shiftKey && key === 'escape') command = 'stop';
    else if (primary) {
      if (key === 'f') command = 'search';
      else if (!event.shiftKey) command = {n: 'new-chat', ',': 'settings', b: 'sidebar', l: 'focus', '/': 'models', m: 'minimize', w: 'close'}[key];
    }
    if (!command || !window.mygo?.call) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    window.mygo.call('WebChatShortcuts.Invoke', command).catch(() => {});
  }, true);

  const editor = () => document.querySelector('#chat-route-main [contenteditable="true"]') || document.querySelector('#chat-route-main textarea');
  const text = node => {
    if (!node.isContentEditable) return node.value;
    const blocks = Array.from(node.children);
    if (blocks.length && blocks.every(block => block.tagName === 'P')) {
      return blocks.map(block => block.textContent ? block.innerText : '').join('\n');
    }
    return node.textContent ? node.innerText : '';
  };
  const notify = () => window.mygo?.call('WebChatShortcuts.DraftChanged').catch(() => {});
  let edited = false, restored = false, restoring = false, lastText = '';
  let session = 0, requested = 0, switching = false, navigation = Promise.resolve();
  const reset = token => {
    session = token;
    pending = started = edited = restored = restoring = false;
    createdURL = lastText = '';
  };
  for (const event of ['input', 'compositionend']) document.addEventListener(event, e => {
    const node = editor();
    if (node && (e.target === node || node.contains(e.target))) {
      const value = text(node);
      if (!restoring && value !== lastText) edited = true;
      lastText = value;
      notify();
    }
  }, true);

  let pending = false, started = false, createdURL = '';
  const observeRoute = () => {
    if (switching || createdURL) return;
    const path = location.pathname;
    if (/^\/chat\/local_\d+$/.test(path)) {
      pending = true;
      if (!started) {
        started = true;
        notify();
      }
    }
    else {
      if (pending && /^\/chat\/\d+$/.test(path)) createdURL = location.origin + path;
      pending = false;
    }
  };
  // Observe the short-lived local route synchronously; host polling can miss it.
  for (const method of ['pushState', 'replaceState']) {
    const original = history[method];
    history[method] = function(...args) {
      const result = original.apply(this, args);
      observeRoute();
      return result;
    };
  }
  window.addEventListener('popstate', observeRoute);
  window.__copilotDoubao = {
    switchTarget(token, target) {
      if (token <= requested) return Promise.resolve(false);
      requested = token;
      switching = true;
      navigation = navigation.catch(() => false).then(async () => {
        if (requested !== token) return false;
        const url = new URL(target);
        if (url.origin !== location.origin || !/^\/chat(?:\/\d+)?\/?$/.test(url.pathname)) return false;
        const blank = /^\/chat\/?$/.test(url.pathname);
        const controls = blank
          ? Array.from(document.querySelectorAll('nav [role="button"], nav button')).filter(node => /^新对话(?:Ctrl|⌘|$)/.test(node.textContent.replace(/\s/g, '')))
          : Array.from(document.querySelectorAll('a[href]')).filter(node => node.getAttribute('href') === url.pathname);
        if (controls.length !== 1) return false;
        const previousEditor = editor();
        const sameRoute = location.pathname.replace(/\/$/, '') === url.pathname.replace(/\/$/, '');
        const main = document.documentElement;
        if (main) main.inert = true;
        controls[0].click();
        // Let the site's router render; URL changes alone precede editor updates.
        let stable = 0;
        for (let i = 0; i < 30; i++) {
          await new Promise(resolve => setTimeout(resolve, 100));
          const node = editor();
          const arrived = location.pathname.replace(/\/$/, '') === url.pathname.replace(/\/$/, '') && node && (blank ? text(node) === '' : sameRoute || node !== previousEditor);
          stable = arrived ? stable + 1 : 0;
          if (stable >= 3) {
            if (requested !== token) return false;
            reset(token);
            switching = false;
            if (main) main.inert = false;
            notify();
            return true;
          }
        }
        return false;
      });
      return navigation;
    },
    snapshot() {
      observeRoute();
      const node = editor();
      return {url: location.href, title: document.title, session, switching, createdURL, started, edited, draft: node && (restored || edited) ? text(node) : null};
    },
    restoreDraft(value, token = 0) {
      if (switching || token !== session) return true;
      const node = editor();
      if (!node) return false;
      if (restored || edited || started || !/^\/chat\/?$/.test(location.pathname)) return true;
      restored = true;
      lastText = text(node);
      if (lastText || !value) return true;
      restoring = true;
      try {
        if (node.isContentEditable) {
          node.focus();
          const selection = window.getSelection();
          const range = document.createRange();
          range.selectNodeContents(node);
          selection.removeAllRanges();
          selection.addRange(range);
          document.execCommand('insertText', false, value);
        } else {
          Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(node, value);
          node.dispatchEvent(new Event('input', {bubbles: true}));
        }
      } finally {
        restoring = false;
        lastText = text(node);
      }
      return true;
    }
  };
  observeRoute();
})();
