import { fromJS } from 'immutable';

import { ACCOUNT_REMOVE_FROM_FOLLOWERS_SUCCESS } from 'mastodon/actions/accounts';

import relationships from '../relationships';

describe('remove from followers', () => {
  it('normalizes the relationship returned by the Mastodon API', () => {
    const state = relationships(undefined, {
      type: ACCOUNT_REMOVE_FROM_FOLLOWERS_SUCCESS,
      relationship: { id: '2', followed_by: false },
    });

    expect(state.get('2')).toEqual(fromJS({ id: '2', followed_by: false }));
  });
});
