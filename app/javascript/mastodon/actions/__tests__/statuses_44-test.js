import { fromJS } from 'immutable';

import api from '../../api';
import { fetchStatus, STATUS_FETCH_FAIL } from '../statuses';
import { deleteFromTimelines } from '../timelines';

jest.mock('../../api', () => ({
  __esModule: true,
  default: jest.fn(),
}));

jest.mock('../timelines', () => ({
  deleteFromTimelines: jest.fn(id => ({ type: 'TEST_TIMELINE_DELETE', id })),
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
