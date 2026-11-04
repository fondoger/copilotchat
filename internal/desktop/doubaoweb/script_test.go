package doubaoweb

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

func TestPageSigningFetchReadAndAbort(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is needed to execute the page transport protocol")
	}
	for _, request := range []struct{ path, contentType string }{
		{"/chat/completion", "application/json"},
		{"/im/conversation/info", "application/json; encoding=utf-8"},
	} {
		t.Run(request.path, func(t *testing.T) {
			fixture := struct {
				Script string     `json:"script"`
				Sign   signInput  `json:"sign"`
				Fetch  fetchInput `json:"fetch"`
			}{
				Script: pageScript,
				Sign:   signInput{Path: request.path, Tab: "test-tab"},
				Fetch: fetchInput{URL: "https://www.doubao.com" + request.path + "?X-Bogus=test", ContentType: request.contentType, CSRF: "csrf",
					Body: base64.StdEncoding.EncodeToString([]byte(`{"text":"测试 🙂"}`))},
			}
			input, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, "-e", pageProtocolHarness)
			cmd.Stdin = bytes.NewReader(input)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("page transport protocol: %v\n%s", err, out)
			}
		})
	}
}

const pageProtocolHarness = `
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fixture = JSON.parse(require('node:fs').readFileSync(0, 'utf8'));
let signature = 'signed', signedQuery, fetchCount = 0, lastFetch;
const chunk = Uint8Array.from({length: 20000}, (_, i) => i % 256);
const state = {
  URL, URLSearchParams, TextDecoder, AbortController, atob, btoa,
  location: new URL('https://www.doubao.com/chat'), navigator: {userAgent: 'test browser'},
  document: {cookie: 'msToken=token; s_v_web_id=fingerprint; passport_csrf_token_default=fallback; passport_csrf_token=current'},
  localStorage: {getItem: key => JSON.stringify({web_id: key === 'samantha_web_web_id' ? 'device' : 'web'})},
  bdms: {frontierSign: async query => {signedQuery = query; return signature}},
  fetch: async (url, options) => {
    fetchCount++; lastFetch = {url, options};
    let read = false;
    return {status: 200, headers: new Map([['content-type', 'text/event-stream']]), body: {getReader: () => ({
      read: async () => {if (read) return {done: true}; read = true; return {done: false, value: chunk}}
    })}};
  }
};
state.window = state;
const command = vm.runInNewContext('(' + fixture.script + ')', state);
(async () => {
  assert.equal((await command('ready')).ready, true);
  for (const [value, key, expected] of [
    ['signed', 'X-Bogus', 'signed'],
    [{a_bogus: 'a', 'X-Bogus': 'x'}, 'a_bogus', 'a'],
    [{'X-Bogus': 'x'}, 'X-Bogus', 'x']
  ]) {
    signature = value;
    const result = await command('sign', fixture.sign);
    const url = new URL(result.url);
    assert.equal(url.origin, 'https://www.doubao.com');
    assert.equal(url.pathname, fixture.sign.path);
    assert.equal(url.searchParams.get(key), expected);
    const query = new URLSearchParams(signedQuery);
    assert.deepEqual([...query.keys()], [...query.keys()].sort());
    for (const [key, value] of Object.entries({device_id:'device', web_id:'web', tea_uuid:'web', fp:'fingerprint', msToken:'token', web_tab_id:'test-tab'})) {
      assert.equal(query.get(key), value);
    }
    assert.equal(result.ua, 'test browser'); assert.equal(result.csrf, 'current');
  }
  for (const invalid of ['', null, {}]) {
    signature = invalid;
    await assert.rejects(command('sign', fixture.sign), /signature/);
  }
  signature = 'signed';
  state.document.cookie = 'passport_csrf_token_default=fallback';
  state.localStorage.getItem = () => 'invalid json';
  const fallback = await command('sign', fixture.sign);
  assert.equal(fallback.csrf, 'fallback');
  assert.equal(new URL(fallback.url).searchParams.has('msToken'), false);

  const header = await command('fetch', fixture.fetch);
  assert.equal(header.status, 200); assert.equal(header.contentType, 'text/event-stream');
  assert.equal(lastFetch.options.body, Buffer.from(fixture.fetch.body, 'base64').toString('utf8'));
  assert.equal(lastFetch.options.method, 'POST'); assert.equal(lastFetch.options.credentials, 'include');
  assert.equal(lastFetch.options.redirect, 'error');
  assert.equal(lastFetch.url, fixture.fetch.url);
  assert.equal(lastFetch.options.headers['Content-Type'], fixture.fetch.contentType);
  assert.equal(lastFetch.options.headers.Accept, 'application/json, text/plain, */*');
  assert.equal(lastFetch.options.headers['x-tt-passport-csrf-token'], 'csrf');
  assert.equal(lastFetch.options.headers['agw-js-conv'], 'str');
  const part = await command('read');
  assert.equal(part.done, false);
  assert.deepEqual(Buffer.from(part.data, 'base64'), Buffer.from(chunk));
  const eof = await command('read');
  assert.equal(eof.done, true); assert.equal(eof.data, '');
  const signal = lastFetch.options.signal;
  await command('abort');
  assert.equal(signal.aborted, true); assert.equal(state.__copilotDoubaoRequest, null);
  await command('abort');
  await assert.rejects(command('read'), /reader/);

  const binary = Buffer.from([0, 255, 13, 10, 128]);
  await command('fetch', {...fixture.fetch, contentType: 'multipart/form-data; boundary=test', csrf: '', body: binary.toString('base64')});
  assert.deepEqual(Buffer.from(lastFetch.options.body), binary);
  assert.equal(lastFetch.options.headers['x-tt-passport-csrf-token'], undefined);
  assert.equal(lastFetch.options.headers.Accept, undefined);

  const before = fetchCount;
  await assert.rejects(command('fetch', {...fixture.fetch, url: 'https://attacker.invalid/chat/completion'}), /origin/);
  state.location = new URL('https://sso.douyin.com/login');
  assert.equal((await command('ready')).ready, false);
  for (const [operation, args] of [['sign', fixture.sign], ['fetch', fixture.fetch], ['read', null]]) {
    await assert.rejects(command(operation, args), /origin/);
  }
  assert.equal(fetchCount, before);
  await command('abort');
})().catch(err => {console.error(err); process.exitCode = 1});
`
