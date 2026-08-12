import { fromJS } from 'immutable';

import api from '../../api';
import { submitCompose } from '../compose';

jest.mock('../../api', () => ({
  __esModule: true,
  default: jest.fn(),
}));

const state = overrides => fromJS({
  compose: {
    id: null,
    idempotencyKey: 'request-id',
    language: 'en',
    media_attachments: [],
    poll: null,
    privacy: 'public',
    sensitive: false,
    spoiler: false,
    spoiler_text: '',
    text: '',
    ...overrides,
  },
});

describe('submitCompose on Mastodon 4.4', () => {
  const request = jest.fn(() => new Promise(() => undefined));
  const response = {
    data: {
      account: { id: '1', username: 'alice' },
      id: '10',
      in_reply_to_id: null,
      tags: [],
      url: 'https://example.com/@alice/10',
      visibility: 'public',
    },
  };

  const flushPromises = () => new Promise(resolve => setTimeout(resolve, 0));

  beforeEach(() => {
    request.mockClear();
    api.mockReturnValue({ request });
  });

  it('does not expose the later quoted_status_id creation contract', () => {
    const getState = () => state({ text: 'ordinary post', quote: { quoted_status: '42' } });

    submitCompose()(jest.fn(), getState);

    expect(request).toHaveBeenCalledTimes(1);
    expect(request.mock.calls[0][0].data).not.toHaveProperty('quoted_status_id');
  });

  it('does not submit a contentless post without a quote', () => {
    submitCompose()(jest.fn(), () => state({}));

    expect(request).not.toHaveBeenCalled();
  });

  it('calls the standalone success callback with the created status', async () => {
    request.mockResolvedValueOnce(response);
    const successCallback = jest.fn();

    submitCompose(undefined, successCallback)(jest.fn(), () => state({ text: 'ordinary post' }));
    await flushPromises();

    expect(successCallback).toHaveBeenCalledTimes(1);
    expect(successCallback).toHaveBeenCalledWith(response.data);
  });

  it('does not call the standalone success callback when publishing fails', async () => {
    request.mockRejectedValueOnce(new Error('request failed'));
    const successCallback = jest.fn();

    submitCompose(undefined, successCallback)(jest.fn(), () => state({ text: 'ordinary post' }));
    await flushPromises();

    expect(successCallback).not.toHaveBeenCalled();
  });
});
