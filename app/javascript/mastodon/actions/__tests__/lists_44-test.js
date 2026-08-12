import { fromJS } from 'immutable';

import api from '../../api';
import {
  fetchListSuggestions,
  followAndAddToListEditor,
  LIST_EDITOR_ADD_SUCCESS,
  LIST_EDITOR_SUGGESTIONS_READY,
  LIST_EDITOR_SUGGESTIONS_REQUEST,
} from '../lists';

jest.mock('../../api', () => ({
  __esModule: true,
  default: jest.fn(),
}));

jest.mock('../accounts', () => ({
  fetchRelationships: jest.fn(ids => ({ type: 'TEST_FETCH_RELATIONSHIPS', ids })),
  followAccountFail: jest.fn(error => ({ type: 'TEST_FOLLOW_FAIL', error })),
  followAccountRequest: jest.fn(id => ({ type: 'TEST_FOLLOW_REQUEST', id })),
  followAccountSuccess: jest.fn(relationship => ({ type: 'TEST_FOLLOW_SUCCESS', relationship })),
}));

jest.mock('../importer', () => ({
  importFetchedAccounts: jest.fn(accounts => ({ type: 'TEST_IMPORT_ACCOUNTS', accounts })),
}));

describe('Mastodon 4.4 list membership actions', () => {
  beforeEach(() => {
    jest.clearAllMocks();
  });

  it('searches all resolvable accounts and fetches their relationships', async () => {
    const accounts = [{ id: '1' }, { id: '2' }];
    const get = jest.fn().mockResolvedValue({ data: accounts });
    const dispatch = jest.fn();
    api.mockReturnValue({ get });

    await fetchListSuggestions('  alice example  ')(dispatch);

    expect(get).toHaveBeenCalledWith('/api/v1/accounts/search', {
      params: { q: 'alice example', resolve: true, limit: 10 },
    });
    expect(dispatch).toHaveBeenCalledWith({
      type: LIST_EDITOR_SUGGESTIONS_REQUEST,
      query: 'alice example',
    });
    expect(dispatch).toHaveBeenCalledWith({
      type: 'TEST_FETCH_RELATIONSHIPS',
      ids: ['1', '2'],
    });
    expect(dispatch).toHaveBeenCalledWith({
      type: LIST_EDITOR_SUGGESTIONS_READY,
      query: 'alice example',
      accounts,
    });
  });

  it('follows before adding an account that is not followed yet', async () => {
    const post = jest.fn()
      .mockResolvedValueOnce({ data: { id: '7', following: true } })
      .mockResolvedValueOnce({ data: {} });
    const getState = () => fromJS({
      listEditor: { listId: '42' },
      accounts: { '7': { locked: false } },
      relationships: { '7': { following: false } },
    });
    const actions = [];
    const dispatch = jest.fn(action => {
      if (typeof action === 'function') {
        return action(dispatch, getState);
      }

      actions.push(action);
      return action;
    });
    api.mockReturnValue({ post });

    await followAndAddToListEditor('7')(dispatch, getState);

    expect(post.mock.calls).toEqual([
      ['/api/v1/accounts/7/follow', { reblogs: true }],
      ['/api/v1/lists/42/accounts', { account_ids: ['7'] }],
    ]);
    expect(actions).toContainEqual({ type: 'TEST_FOLLOW_REQUEST', id: '7' });
    expect(actions).toContainEqual(expect.objectContaining({ type: 'TEST_FOLLOW_SUCCESS' }));
    expect(actions).toContainEqual({
      type: LIST_EDITOR_ADD_SUCCESS,
      listId: '42',
      accountId: '7',
    });
  });
});
