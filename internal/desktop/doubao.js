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
    if (createdURL) return;
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
    snapshot() {
      observeRoute();
      const node = editor();
      return {url: location.href, title: document.title, createdURL, started, edited, draft: node && (restored || edited) ? text(node) : null};
    },
    restoreDraft(value) {
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
