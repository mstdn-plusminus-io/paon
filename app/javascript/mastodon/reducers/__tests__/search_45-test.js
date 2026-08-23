import { fromJS } from 'immutable';

import { SEARCH_FETCH_REQUEST } from '../../actions/search';
import reducer from '../search';

describe('Mastodon 4.5 search request state', () => {
  it('clears stale results while a different query is loading', () => {
    const state = fromJS({
      results: { accounts: ['old'] },
      isLoading: false,
    });

    const next = reducer(state, { type: SEARCH_FETCH_REQUEST, searchType: 'accounts' });

    expect(next.get('isLoading')).toBe(true);
    expect(next.get('results').isEmpty()).toBe(true);
  });
});
