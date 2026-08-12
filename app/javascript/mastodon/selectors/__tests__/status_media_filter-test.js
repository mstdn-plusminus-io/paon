import { fromJS } from 'immutable';

import { makeGetStatus } from '../index';

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
    expect(status.get('matched_media_filters').toJS()).toEqual(['Media filter']);
  });

  it('keeps warn matches as content warnings', () => {
    const status = selectStatus({ filterAction: 'warn' });

    expect(status.get('matched_filters').toJS()).toEqual(['Media filter']);
    expect(status.get('matched_media_filters')).toBe(false);
  });

  it('does not apply a filter outside its configured context', () => {
    const status = selectStatus({ filterAction: 'blur', contextType: 'thread' });

    expect(status.get('matched_filters')).toBe(false);
    expect(status.get('matched_media_filters')).toBe(false);
  });
});
