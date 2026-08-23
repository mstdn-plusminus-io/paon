import { fromJS } from 'immutable';

import reducer from '../compose';

describe('Mastodon 4.5 compose quote defaults', () => {
  it('hydrates the user quote policy into the current composer', () => {
    const state = reducer(undefined, {
      type: 'STORE_HYDRATE',
      state: fromJS({
        compose: {
          default_quote_policy: 'followers',
        },
      }),
    });

    expect(state.get('default_quote_policy')).toBe('followers');
    expect(state.get('quote_policy')).toBe('followers');
  });
});
