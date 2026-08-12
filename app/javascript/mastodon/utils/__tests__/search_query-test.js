import {
  buildSearchQuery,
  normalizeSearchType,
  parseSearchQuery,
} from '../search_query';

describe('search query parameters', () => {
  it('round-trips operators and internal spaces without changing the query', () => {
    const search = buildSearchQuery(
      '  from:alice@example.com two words has:quote  ',
      'statuses',
    );

    expect(parseSearchQuery(search)).toEqual({
      q: 'from:alice@example.com two words has:quote',
      type: 'statuses',
    });
  });

  it('drops invalid types and returns an empty query for whitespace', () => {
    expect(normalizeSearchType('everything')).toBeUndefined();
    expect(buildSearchQuery('   ', 'statuses')).toBe('');
    expect(parseSearchQuery('?q=paon&type=everything')).toEqual({
      q: 'paon',
      type: undefined,
    });
  });
});
