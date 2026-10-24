(() => {
  const config = __WEB_CHAT_CONFIG__;
  if (!config.chat || window !== window.top || location.origin !== 'https://' + config.host || window.__copilotWebChat) return;

  const uuid = '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}';
  const sites = {
    'doubao-web': {blank:/^\/chat\/?$/, remote:/^\/chat\/\d+$/, editor:'#chat-route-main [contenteditable="true"], #chat-route-main textarea', css:'#chat-route-main > :first-child, div[data-header-host="main"]', fresh:'nav [role="button"], nav button'},
    'deepseek-web': {blank:/^\/$/, remote:new RegExp('^/a/chat/s/'+uuid+'$'), editor:'textarea[placeholder="Message DeepSeek"], textarea[placeholder="给 DeepSeek 发送消息"]', css:'#root > .cb86951c > .cddfb2ed, .c3ecdb44 > .dc04ec1d, .c3ecdb44 > ._4cbcd96', fresh:'[tabindex="0"]'},
    'claude-web': {blank:/^\/new\/?$/, remote:new RegExp('^/chat/'+uuid+'$'), editor:'[data-testid="chat-input"][contenteditable="true"]', css:'aside[aria-label="Sidebar"], header.dframe-header', fresh:'aside a[href="/new"]'},
    'gemini-web': {blank:/^\/(?:u\/\d+\/)?app\/?$/, remote:/^\/(?:u\/\d+\/)?app\/[0-9a-f]+$/, editor:'.ql-editor[role="textbox"][contenteditable="true"]', css:'nav[aria-label="Side Navigation"], side-navigation, .conversation-title-container', fresh:'a[href="/app"][aria-label="New chat"], nav a[href="/app"]'},
    'chatgpt-web': {blank:/^\/$/, remote:new RegExp('^/c/'+uuid+'$'), editor:'[contenteditable="true"][data-composer-markdown], #prompt-textarea[contenteditable="true"]', css:'aside.app-shell-left-panel, nav[aria-label="Chat history"], #stage-slideover-sidebar, #sidebar-header, #browser-sidebar-popover, header', fresh:'button[aria-label="New chat"], a[data-testid="create-new-chat-button"]'}
  };
  const site = sites[config.kind];
  if (!site) return;
  const hideSidebar = () => {
    const style = document.createElement('style');
    style.textContent = site.css + ' { display: none !important; }';
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

  const editor = () => config.kind === 'doubao-web'
    ? document.querySelector('#chat-route-main [contenteditable="true"]') || document.querySelector('#chat-route-main textarea')
    : document.querySelector(site.editor);
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
    intentUntil = 0;
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

  let pending = false, started = false, createdURL = '', intentUntil = 0;
  const armCreation = event => {
    if (!event.isTrusted || event.isComposing || switching || !site.blank.test(location.pathname)) return;
    const node = editor();
    if (!node) return;
    const target = event.target;
    const inEditor = target === node || node.contains(target);
    const send = target.closest?.('button[type="submit"], button[data-testid="send-button"], button[aria-label="Send message"], button[aria-label="Send Message"], button[aria-label="Send"], button[aria-label="发送"], button.send-button, [role="button"]._52c986b');
    if (event.type === 'keydown' && (!inEditor || event.key !== 'Enter' || event.shiftKey || event.ctrlKey || event.metaKey || event.altKey)) return;
    if (event.type === 'click' && !send) {
      if (target.closest?.('a[href], button, [role="button"]')) intentUntil = 0;
      return;
    }
    if (event.type === 'submit' && !target.contains?.(node)) return;
    // Explicit send controls can submit attachments without accompanying text.
    if (event.type === 'keydown' && !text(node).trim()) return;
    intentUntil = Date.now() + 60000;
  };
  if (config.kind !== 'doubao-web') {
    document.addEventListener('keydown', armCreation, true);
    document.addEventListener('click', armCreation, true);
    document.addEventListener('submit', armCreation, true);
  }
  const observeRoute = () => {
    if (switching || createdURL) return;
    const path = location.pathname;
    if (config.kind !== 'doubao-web') {
      if (site.remote.test(path)) {
        if (intentUntil > Date.now()) {
          createdURL = location.origin + path;
          started = true;
          notify();
        }
        intentUntil = 0;
      } else if (config.kind === 'chatgpt-web' && /^\/c\/local-chatgpt(?:%3A|:)[0-9a-f-]+$/i.test(path) && intentUntil > Date.now()) {
        started = true;
        notify();
      } else if (!site.blank.test(path)) intentUntil = 0;
      return;
    }
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
  window.addEventListener('popstate', () => {
    intentUntil = 0;
    observeRoute();
  });
  window.__copilotWebChat = {
    switchTarget(token, target) {
      if (token <= requested) return Promise.resolve(false);
      requested = token;
      switching = true;
      navigation = navigation.catch(() => false).then(async () => {
        if (requested !== token) return false;
        const url = new URL(target);
        if (url.origin !== location.origin || !(site.blank.test(url.pathname) || site.remote.test(url.pathname))) return false;
        const blank = site.blank.test(url.pathname);
        if (!blank && ['gemini-web', 'chatgpt-web'].includes(config.kind) && !Array.from(document.querySelectorAll('a[href]')).some(node => node.getAttribute('href') === url.pathname)) {
          const toggle = document.querySelector(config.kind === 'gemini-web'
            ? 'button[aria-label="Main menu"], button[data-test-id="side-nav-sparkle-button"][aria-label="Open sidebar"]'
            : 'button[aria-label="Show sidebar"]');
          if (toggle) {
            toggle.click();
            for (let i = 0; i < 10; i++) {
              await new Promise(resolve => setTimeout(resolve, 100));
              if (requested !== token) return false;
              if (Array.from(document.querySelectorAll('a[href]')).some(node => node.getAttribute('href') === url.pathname)) break;
            }
          }
        }
        let controls = blank
          ? Array.from(document.querySelectorAll(site.fresh))
          : Array.from(document.querySelectorAll('a[href]')).filter(node => node.getAttribute('href') === url.pathname);
        if (blank && config.kind === 'doubao-web') controls = controls.filter(node => /^新对话(?:Ctrl|⌘|$)/.test(node.textContent.replace(/\s/g, '')));
        if (blank && config.kind === 'deepseek-web') controls = controls.filter(node => /^(New chat|新对话|开启新对话)/i.test(node.textContent.trim()));
        if (blank && config.kind === 'gemini-web') {
          controls = Array.from(document.querySelectorAll('a[href]')).filter(node => node.getAttribute('href') === url.pathname);
        }
        if (blank && config.kind === 'claude-web') controls = controls.filter(node => /New|新对话|新建/.test(node.textContent) && node.getAttribute('aria-label') !== 'Home');
        if (blank && config.kind === 'chatgpt-web' && controls.length > 1) {
          const labeled = controls.filter(node => /^(New chat|新聊天|新对话)$/.test(node.textContent.trim()));
          // Responsive layouts render duplicate new-chat buttons with the same action.
          controls = (labeled.length ? labeled : controls).slice(0, 1);
        }
        if (controls.length !== 1) return false;
        const message = () => config.kind === 'chatgpt-web'
          ? document.querySelector('[data-chatgpt-search-message-ids], [data-message-id]')
          : config.kind === 'gemini-web' ? document.querySelector('user-query') : null;
        const previousMessage = message();
        const previousEditor = editor();
        const sameRoute = location.pathname.replace(/\/$/, '') === url.pathname.replace(/\/$/, '');
        const main = document.documentElement;
        if (main) main.inert = true;
        controls[0].click();
        // Let the site's router render; URL changes alone precede editor updates.
        let stable = 0;
        for (let i = 0; i < 60; i++) {
          await new Promise(resolve => setTimeout(resolve, 100));
          const node = editor();
          const arrived = location.pathname.replace(/\/$/, '') === url.pathname.replace(/\/$/, '') && node && (blank ? text(node) === '' : sameRoute || node !== previousEditor || message() && message() !== previousMessage);
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
      if (restored || edited || started || !site.blank.test(location.pathname)) return true;
      restored = true;
      lastText = text(node);
      if (lastText === value) return true;
      restoring = true;
      try {
        if (node.isContentEditable) {
          node.focus();
          const selection = window.getSelection();
          const range = document.createRange();
          range.selectNodeContents(node);
          selection.removeAllRanges();
          selection.addRange(range);
          document.execCommand(value ? 'insertText' : 'delete', false, value);
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
