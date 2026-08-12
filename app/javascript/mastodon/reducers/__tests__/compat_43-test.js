import { ACCOUNT_BLOCK_SUCCESS } from 'mastodon/actions/accounts';
import { DOMAIN_BLOCK_SUCCESS } from 'mastodon/actions/domain_blocks';
import { SUGGESTIONS_DISMISS, SUGGESTIONS_FETCH_SUCCESS } from 'mastodon/actions/suggestions';
import { TIMELINE_EXPAND_SUCCESS, TIMELINE_INSERT, TIMELINE_SUGGESTIONS } from 'mastodon/actions/timelines';

import suggestionsReducer from '../suggestions';
import timelinesReducer from '../timelines';

describe('Mastodon 4.3 follow suggestion state', () => {
  it('keeps source hints and removes immutable suggestion records for dismiss and moderation actions', () => {
    let state = suggestionsReducer(undefined, {
      type: SUGGESTIONS_FETCH_SUCCESS,
      suggestions: [
        { account: { id: '1' }, sources: ['featured'] },
        { account: { id: '2' }, sources: ['friends_of_friends'] },
        { account: { id: '3' }, sources: ['most_followed'] },
      ],
    });

    expect(state.getIn(['items', 0, 'sources']).toJS()).toEqual(['featured']);

    state = suggestionsReducer(state, { type: SUGGESTIONS_DISMISS, id: '1' });
    state = suggestionsReducer(state, { type: ACCOUNT_BLOCK_SUCCESS, relationship: { id: '2' } });
    state = suggestionsReducer(state, { type: DOMAIN_BLOCK_SUCCESS, accounts: ['3'] });

    expect(state.get('items').isEmpty()).toBe(true);
  });
});

describe('Mastodon 4.3 inline suggestions timeline marker', () => {
  const expand = (statuses, next = '/api/v1/timelines/home?max_id=1') => ({
    type: TIMELINE_EXPAND_SUCCESS,
    timeline: 'home',
    statuses,
    next,
    partial: false,
    isLoadingRecent: false,
    usePendingItems: false,
  });

  it('inserts the carousel once and preserves it when older statuses are loaded', () => {
    let state = timelinesReducer(undefined, expand([{ id: '300' }, { id: '200' }, { id: '100' }]));

    state = timelinesReducer(state, {
      type: TIMELINE_INSERT,
      timeline: 'home',
      key: TIMELINE_SUGGESTIONS,
      index: 2,
    });
    state = timelinesReducer(state, {
      type: TIMELINE_INSERT,
      timeline: 'home',
      key: TIMELINE_SUGGESTIONS,
      index: 1,
    });

    expect(state.getIn(['home', 'items']).toJS()).toEqual(['300', '200', TIMELINE_SUGGESTIONS, '100']);

    state = timelinesReducer(state, expand([{ id: '90' }], null));

    expect(state.getIn(['home', 'items']).toJS()).toEqual(['300', '200', TIMELINE_SUGGESTIONS, '100', '90']);
  });
});
