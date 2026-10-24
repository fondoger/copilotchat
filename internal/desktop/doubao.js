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

  let pending = false, createdURL = '';
  const observeRoute = () => {
    if (createdURL) return;
    const path = location.pathname;
    if (/^\/chat\/local_\d+$/.test(path)) pending = true;
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
      return {url: location.href, title: document.title, createdURL};
    }
  };
  observeRoute();
})();
