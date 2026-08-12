const SEARCH_TYPES = new Set(['accounts', 'statuses', 'hashtags']);

export const normalizeSearchType = type =>
  SEARCH_TYPES.has(type) ? type : undefined;

export const parseSearchQuery = search => {
  const params = new URLSearchParams(search);

  return {
    q: (params.get('q') ?? '').trim(),
    type: normalizeSearchType(params.get('type')),
  };
};

export const buildSearchQuery = (query, type) => {
  const q = query.trim();

  if (q.length === 0) {
    return '';
  }

  const params = new URLSearchParams({ q });
  const normalizedType = normalizeSearchType(type);
  if (normalizedType) {
    params.set('type', normalizedType);
  }

  return params.toString();
};
