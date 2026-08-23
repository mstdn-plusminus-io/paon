import { fromJS } from 'immutable';

import api from '../../api';
import {
  deleteStatus,
  fetchStatus,
  STATUS_DELETE_FAIL,
  STATUS_DELETE_SUCCESS,
  STATUS_FETCH_FAIL,
} from '../statuses';
import { deleteFromTimelines } from '../timelines';

jest.mock('../../api', () => ({
  __esModule: true,
  default: jest.fn(),
}));

jest.mock('../timelines', () => ({
  deleteFromTimelines: jest.fn(id => ({ type: 'TEST_TIMELINE_DELETE', id })),
}));

jest.mock('../importer', () => ({
  importFetchedAccount: jest.fn(account => ({ type: 'TEST_IMPORT_ACCOUNT', account })),
  importFetchedStatus: jest.fn(status => ({ type: 'TEST_IMPORT_STATUS', status })),
  importFetchedStatuses: jest.fn(statuses => ({ type: 'TEST_IMPORT_STATUSES', statuses })),
}));

const flushPromises = () => new Promise(resolve => setTimeout(resolve, 0));

describe('fetchStatus on Mastodon 4.4', () => {
  const get = jest.fn();

  beforeEach(() => {
    get.mockReset();
    deleteFromTimelines.mockClear();
    api.mockReturnValue({ get });
  });

  it('removes a 404 status from timelines', async () => {
    get.mockRejectedValueOnce({ response: { status: 404 } });
    const dispatch = jest.fn();

    fetchStatus('42', true, true)(dispatch, () => fromJS({ statuses: {} }));
    await flushPromises();

    expect(dispatch).toHaveBeenCalledWith(expect.objectContaining({
      type: STATUS_FETCH_FAIL,
      id: '42',
      status: 404,
    }));
    expect(deleteFromTimelines).toHaveBeenCalledWith('42');
    expect(dispatch).toHaveBeenCalledWith({ type: 'TEST_TIMELINE_DELETE', id: '42' });
  });

  it('does not remove a transiently unavailable status from timelines', async () => {
    get.mockRejectedValueOnce({ response: { status: 503 } });
    const dispatch = jest.fn();

    fetchStatus('42', true, true)(dispatch, () => fromJS({ statuses: {} }));
    await flushPromises();

    expect(deleteFromTimelines).not.toHaveBeenCalled();
  });
});

describe('deleteStatus on Mastodon 4.5', () => {
  const del = jest.fn();

  beforeEach(() => {
    del.mockReset();
    api.mockReturnValue({ delete: del });
  });

  it('returns a promise so the detail view can leave only after deletion succeeds', async () => {
    del.mockResolvedValueOnce({ data: { account: { id: '1' }, text: '' } });
    const dispatch = jest.fn();
    const pending = deleteStatus('42')(dispatch, () => fromJS({
      statuses: { 42: { id: '42', poll: null } },
    }));

    await expect(pending).resolves.toMatchObject({ data: { text: '' } });
    expect(dispatch).toHaveBeenCalledWith(expect.objectContaining({
      type: STATUS_DELETE_SUCCESS,
      id: '42',
    }));
  });

  it('rejects after recording failure so the detail view remains in place', async () => {
    const error = new Error('delete failed');
    del.mockRejectedValueOnce(error);
    const dispatch = jest.fn();
    const pending = deleteStatus('42')(dispatch, () => fromJS({
      statuses: { 42: { id: '42', poll: null } },
    }));

    await expect(pending).rejects.toBe(error);
    expect(dispatch).toHaveBeenCalledWith(expect.objectContaining({
      type: STATUS_DELETE_FAIL,
      id: '42',
    }));
  });
});
