import { normalizeFamiliarFollowersResponse } from 'mastodon/actions/accounts_familiar_followers';

import reducer from '../accounts_familiar_followers';

describe('Mastodon 4.4 familiar followers state', () => {
  it('normalizes a missing or null API result as an empty list', () => {
    expect(normalizeFamiliarFollowersResponse('42', null)).toEqual({ id: '42', accounts: [] });
    expect(normalizeFamiliarFollowersResponse('42', [{ id: '7', accounts: [{ id: '1' }] }])).toEqual({ id: '42', accounts: [] });
  });

  it('tracks loading and stores an empty successful result', () => {
    let state = reducer(undefined, {
      type: 'ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_REQUEST',
      id: '42',
    });

    expect(state.getIn(['42', 'isLoading'])).toBe(true);

    state = reducer(state, {
      type: 'ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_SUCCESS',
      id: '42',
      accountIds: [],
    });

    expect(state.getIn(['42', 'loaded'])).toBe(true);
    expect(state.getIn(['42', 'isLoading'])).toBe(false);
    expect(state.getIn(['42', 'accountIds']).isEmpty()).toBe(true);
  });
});
