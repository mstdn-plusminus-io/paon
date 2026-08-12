import { fromJS, OrderedSet } from 'immutable';

import { searchHistory } from 'mastodon/settings';

import {
  forgetSearchResult,
  searchExpansionOffset,
  searchResultsHaveMore,
  setLibraryIfNeeded,
} from '../search';

jest.mock('mastodon/initial_state', () => ({ searchEnabled: true }));

describe('Mastodon 4.4 search semantics', () => {
  it('preserves explicit search scopes and adds library scope only when needed', () => {
    expect(setLibraryIfNeeded('two words')).toBe('two words in:library');
    expect(setLibraryIfNeeded('two words in:all')).toBe('two words in:all');
    expect(setLibraryIfNeeded('within:example')).toBe(
      'within:example in:library',
    );
    expect(setLibraryIfNeeded('@alice@example.com')).toBe(
      '@alice@example.com',
    );
  });

  it('continues pagination after each ten-result page and its peek item', () => {
    expect(searchExpansionOffset(11)).toBe(10);
    expect(searchExpansionOffset(21)).toBe(20);
    expect(searchResultsHaveMore(11)).toBe(true);
    expect(searchResultsHaveMore(21)).toBe(true);
    expect(searchResultsHaveMore(20)).toBe(false);
  });

  it('forgets only the selected recent-search type', () => {
    const recent = OrderedSet([
      fromJS({ q: 'paon', type: 'accounts' }),
      fromJS({ q: 'paon', type: 'statuses' }),
    ]);
    const dispatch = jest.fn();
    const set = jest.spyOn(searchHistory, 'set').mockImplementation(() => undefined);

    forgetSearchResult({ q: 'paon', type: 'accounts' })(dispatch, () => fromJS({
      meta: { me: '1' },
      search: { recent },
    }));

    const action = dispatch.mock.calls[0][0];
    expect(action.recent.size).toBe(1);
    expect(action.recent.first().toJS()).toEqual({ q: 'paon', type: 'statuses' });
    set.mockRestore();
  });
});
