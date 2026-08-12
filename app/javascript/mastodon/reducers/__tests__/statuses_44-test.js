import { fromJS } from 'immutable';

import {
  STATUS_FETCH_FAIL,
  STATUS_FETCH_REQUEST,
} from '../../actions/statuses';
import statuses from '../statuses';

describe('Mastodon 4.4 status refetch failures', () => {
  it('keeps an already-known status on a transient refetch failure', () => {
    let state = fromJS({
      '42': { id: '42', content: 'Known post' },
    });

    state = statuses(state, { type: STATUS_FETCH_REQUEST, id: '42' });
    state = statuses(state, { type: STATUS_FETCH_FAIL, id: '42', status: 503 });

    expect(state.getIn(['42', 'content'])).toBe('Known post');
    expect(state.hasIn(['42', 'isLoading'])).toBe(false);
  });

  it('keeps an accepted known quote visible on a transient refetch failure', () => {
    let state = fromJS({
      '10': { id: '10', quote: { state: 'accepted', quoted_status: '42' } },
      '42': { id: '42', content: 'Known quoted post' },
    });

    state = statuses(state, { type: STATUS_FETCH_REQUEST, id: '42' });
    state = statuses(state, {
      type: STATUS_FETCH_FAIL,
      id: '42',
      parentQuotePostId: '10',
      status: 503,
    });

    expect(state.getIn(['42', 'content'])).toBe('Known quoted post');
    expect(state.getIn(['10', 'quote', 'state'])).toBe('accepted');
  });

  it('removes a loading stub when the status was not already known', () => {
    let state = statuses(undefined, { type: STATUS_FETCH_REQUEST, id: '42' });
    state = statuses(state, { type: STATUS_FETCH_FAIL, id: '42', status: 503 });

    expect(state.has('42')).toBe(false);
  });
});
