// Example service contract: GET /models returns {data: [{id, supported_endpoints}]}.
const catalogs = ['https://models.example.org/models', 'https://region.example.org/models'];
const protocols = ['/v1/messages', '/responses', '/chat/completions'];

export function catalog() {
  return { url: catalogs[0], redirects: catalogs };
}

export function models(body) {
  if (!Array.isArray(body.data)) throw new Error('Missing catalog data');
  const result = [];
  for (const entry of body.data) {
    if (!entry.id || entry.policy?.state === 'disabled') continue;
    const endpoint = protocols.find(path => entry.supported_endpoints?.includes(path));
    if (endpoint) result.push({ ...host.model(entry, endpoint), picker: entry.model_picker_enabled !== false });
  }
  return result;
}

export function inference({ catalogURL, endpoint, conversationId }) {
  if (!catalogs.includes(catalogURL)) throw new Error('Unknown catalog');
  return { url: catalogURL.slice(0, -'/models'.length) + endpoint, headers: { 'X-ConversationId': conversationId } };
}
