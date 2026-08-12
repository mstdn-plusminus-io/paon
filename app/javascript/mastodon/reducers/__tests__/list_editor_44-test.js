import {
  LIST_ACCOUNTS_FETCH_FAIL,
  LIST_EDITOR_ADD_FAIL,
  LIST_EDITOR_ADD_REQUEST,
  LIST_EDITOR_SUGGESTIONS_CHANGE,
  LIST_EDITOR_SUGGESTIONS_READY,
  LIST_EDITOR_SUGGESTIONS_REQUEST,
} from '../../actions/lists';
import listEditor from '../list_editor';

describe('Mastodon 4.4 list editor state', () => {
  it('ignores a stale search response', () => {
    let state = listEditor(undefined, {
      type: LIST_EDITOR_SUGGESTIONS_CHANGE,
      value: 'alice',
    });
    state = listEditor(state, {
      type: LIST_EDITOR_SUGGESTIONS_REQUEST,
      query: 'alice',
    });
    state = listEditor(state, {
      type: LIST_EDITOR_SUGGESTIONS_CHANGE,
      value: 'bob',
    });
    state = listEditor(state, {
      type: LIST_EDITOR_SUGGESTIONS_READY,
      query: 'alice',
      accounts: [{ id: '1' }],
    });

    expect(state.getIn(['suggestions', 'items']).isEmpty()).toBe(true);
    expect(state.getIn(['suggestions', 'loaded'])).toBe(false);
  });

  it('tracks pending membership operations and exposes failures', () => {
    let state = listEditor(undefined, {
      type: LIST_EDITOR_ADD_REQUEST,
      accountId: '1',
    });

    expect(state.getIn(['accounts', 'pending']).has('1')).toBe(true);

    state = listEditor(state, {
      type: LIST_EDITOR_ADD_FAIL,
      accountId: '1',
    });

    expect(state.getIn(['accounts', 'pending']).has('1')).toBe(false);

    state = listEditor(state, { type: LIST_ACCOUNTS_FETCH_FAIL });
    expect(state.getIn(['accounts', 'loaded'])).toBe(true);
    expect(state.getIn(['accounts', 'error'])).toBe(true);
  });
});
