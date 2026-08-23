import { fromJS } from 'immutable';

import { makeGetStatus, makeGetStatusWithExtraInfo } from '../index';

const selectStatus = ({ filterAction, contextType = 'home' }) => {
  const state = fromJS({
    accounts: {
      7: { id: '7', username: 'alice' },
    },
    accounts_counters: {},
    filters: {
      42: {
        id: '42',
        title: 'Media filter',
        context: ['home'],
        filter_action: filterAction,
        expires_at: null,
      },
    },
    statuses: {
      1: {
        id: '1',
        account: '7',
        filtered: [{ filter: '42', keyword_matches: ['cats'] }],
      },
    },
  });

  return makeGetStatus()(state, { id: '1', contextType });
};

describe('makeGetStatus media filters', () => {
  it('keeps a blur match separate from the content warning matches', () => {
    const status = selectStatus({ filterAction: 'blur' });

    expect(status.get('matched_filters')).toBe(false);
    expect(status.get('matched_media_filters')).toEqual(['Media filter']);
  });

  it('keeps warn matches as content warnings', () => {
    const status = selectStatus({ filterAction: 'warn' });

    expect(status.get('matched_filters')).toEqual(['Media filter']);
    expect(status.get('matched_media_filters')).toBe(false);
  });

  it('does not apply a filter outside its configured context', () => {
    const status = selectStatus({ filterAction: 'blur', contextType: 'thread' });

    expect(status.get('matched_filters')).toBe(false);
    expect(status.get('matched_media_filters')).toBe(false);
  });
});

describe('makeGetStatus Mastodon 4.5 loading and detail states', () => {
  it('keeps a known status visible while it is being refreshed', () => {
    const state = fromJS({
      accounts: { 7: { id: '7', username: 'alice' } },
      filters: {},
      statuses: {
        1: { id: '1', account: '7', isLoading: true, visibility: 'public' },
      },
    });

    const result = makeGetStatusWithExtraInfo()(state, { id: '1', contextType: 'home' });

    expect(result.loadingState).toBe('loading');
    expect(result.status.get('id')).toBe('1');
  });

  it('distinguishes a filtered status from a missing status', () => {
    const state = fromJS({
      accounts: { 7: { id: '7', username: 'alice' } },
      filters: {
        42: {
          id: '42',
          title: 'Hidden posts',
          context: ['home'],
          filter_action: 'hide',
          expires_at: null,
        },
      },
      statuses: {
        1: { id: '1', account: '7', filtered: [{ filter: '42' }] },
      },
    });

    const result = makeGetStatusWithExtraInfo()(state, { id: '1', contextType: 'home' });

    expect(result).toEqual({ status: null, loadingState: 'filtered' });
  });
});
