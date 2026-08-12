import api from '../../api';
import {
  ACCOUNT_REMOVE_FROM_FOLLOWERS_REQUEST,
  ACCOUNT_REMOVE_FROM_FOLLOWERS_SUCCESS,
  removeAccountFromFollowers,
} from '../accounts';

jest.mock('../../api', () => ({
  __esModule: true,
  default: jest.fn(),
}));

const flushPromises = () => new Promise(resolve => setTimeout(resolve, 0));

describe('removeAccountFromFollowers', () => {
  it('uses the official API and stores its returned relationship', async () => {
    const relationship = { id: '42', followed_by: false };
    const post = jest.fn().mockResolvedValue({ data: relationship });
    const dispatch = jest.fn();
    api.mockReturnValue({ post });

    removeAccountFromFollowers('42')(dispatch);
    await flushPromises();

    expect(post).toHaveBeenCalledWith('/api/v1/accounts/42/remove_from_followers');
    expect(dispatch).toHaveBeenNthCalledWith(1, {
      type: ACCOUNT_REMOVE_FROM_FOLLOWERS_REQUEST,
      id: '42',
      skipLoading: true,
    });
    expect(dispatch).toHaveBeenNthCalledWith(2, {
      type: ACCOUNT_REMOVE_FROM_FOLLOWERS_SUCCESS,
      relationship,
      skipLoading: true,
    });
  });
});
