import { fromJS } from 'immutable';

import api from '../../api';
import { ALERT_SHOW } from '../alerts';
import {
  COMPOSE_QUOTE,
  COMPOSE_SUGGESTION_SELECT,
  quoteCompose,
  selectComposeSuggestion,
  submitCompose,
} from '../compose';

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
    quote_policy: 'followers',
    quoted_status_id: null,
    sensitive: false,
    spoiler: false,
    spoiler_text: '',
    text: '',
    ...overrides,
  },
});

describe('submitCompose on Mastodon 4.5', () => {
  const request = jest.fn(() => new Promise(() => undefined));
  const get = jest.fn();
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
    get.mockReset();
    api.mockReturnValue({ get, request });
  });

  it('submits the official quote target and approval policy', () => {
    const getState = () => state({ text: 'quoted post', quoted_status_id: '42' });

    submitCompose()(jest.fn(), getState);

    expect(request).toHaveBeenCalledTimes(1);
    expect(request.mock.calls[0][0].data).toMatchObject({
      quoted_status_id: '42',
      quote_approval_policy: 'followers',
    });
  });

  it.each(['private', 'direct'])('forces quote approval to nobody for %s posts', privacy => {
    const getState = () => state({ text: 'restricted post', privacy, quote_policy: 'public' });

    submitCompose()(jest.fn(), getState);

    expect(request.mock.calls[0][0].data.quote_approval_policy).toBe('nobody');
  });

  it('does not submit a quote without commentary or a content warning', () => {
    submitCompose()(jest.fn(), () => state({ quoted_status_id: '42' }));

    expect(request).not.toHaveBeenCalled();
  });

  it('allows a quote with a content warning and no body text', () => {
    submitCompose()(jest.fn(), () => state({
      quoted_status_id: '42',
      spoiler: true,
      spoiler_text: 'Context',
    }));

    expect(request).toHaveBeenCalledTimes(1);
    expect(request.mock.calls[0][0].data.quoted_status_id).toBe('42');
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

describe('quoteCompose on Mastodon 4.5', () => {
  const get = jest.fn();
  const flushPromises = () => new Promise(resolve => setTimeout(resolve, 0));

  beforeEach(() => {
    get.mockReset();
    api.mockReturnValue({ get });
  });

  it('uses a known automatic quote approval without another request', () => {
    const status = fromJS({
      id: '42',
      quote_approval: { current_user: 'automatic' },
      visibility: 'public',
    });
    const dispatch = jest.fn();

    quoteCompose(status)(dispatch, () => state({ mounted: true }));

    expect(get).not.toHaveBeenCalled();
    expect(dispatch).toHaveBeenCalledWith({ type: COMPOSE_QUOTE, status });
  });

  it('keeps an existing poll and refuses to prepare a quote in the composer', () => {
    const status = fromJS({ id: '42', quote_approval: { current_user: 'automatic' }, visibility: 'public' });
    const dispatch = jest.fn();

    quoteCompose(status)(dispatch, () => state({ poll: { options: ['yes', 'no'] } }));

    expect(dispatch).toHaveBeenCalledWith(expect.objectContaining({
      type: ALERT_SHOW,
      alert: expect.objectContaining({
        message: expect.objectContaining({ defaultMessage: 'Quoting is not allowed with polls.' }),
      }),
    }));
    expect(dispatch).not.toHaveBeenCalledWith(expect.objectContaining({ type: COMPOSE_QUOTE }));
  });

  it('keeps existing media and refuses to prepare a quote in the composer', () => {
    const status = fromJS({ id: '42', quote_approval: { current_user: 'automatic' }, visibility: 'public' });
    const dispatch = jest.fn();

    quoteCompose(status)(dispatch, () => state({ media_attachments: [{ id: '7' }] }));

    expect(dispatch).toHaveBeenCalledWith(expect.objectContaining({
      type: ALERT_SHOW,
      alert: expect.objectContaining({
        message: expect.objectContaining({ defaultMessage: 'Quoting is not allowed with media attachments.' }),
      }),
    }));
    expect(dispatch).not.toHaveBeenCalledWith(expect.objectContaining({ type: COMPOSE_QUOTE }));
  });

  it('fetches the regular REST Status when a public stream omits quote approval', async () => {
    get.mockResolvedValueOnce({ data: { id: '42' } });
    const status = fromJS({ id: '42', visibility: 'public' });
    const dispatch = jest.fn();

    quoteCompose(status)(dispatch, () => state({ mounted: true }));
    await flushPromises();

    expect(get).toHaveBeenCalledWith('/api/v1/statuses/42');
    expect(dispatch).toHaveBeenCalledWith(expect.any(Function));
    expect(dispatch).not.toHaveBeenCalledWith(expect.objectContaining({ type: COMPOSE_QUOTE }));
  });
});

describe('Mastodon 4.5 compose completions', () => {
  it('preserves the typed hashtag prefix and completes the suggestion', () => {
    const dispatch = jest.fn();

    selectComposeSuggestion(
      4,
      '#mAS',
      { type: 'hashtag', name: 'Mastodon' },
      ['text'],
    )(dispatch, () => fromJS({}));

    expect(dispatch).toHaveBeenCalledWith({
      type: COMPOSE_SUGGESTION_SELECT,
      position: 3,
      token: '#mAS',
      completion: '#mAStodon',
      path: ['text'],
    });
  });

  it('normalizes an account completion to a complete @mention', () => {
    const dispatch = jest.fn();

    selectComposeSuggestion(
      4,
      '@ali',
      { type: 'account', id: '7' },
      ['text'],
    )(dispatch, () => fromJS({ accounts: { 7: { acct: 'alice@example.com' } } }));

    expect(dispatch).toHaveBeenCalledWith(expect.objectContaining({
      type: COMPOSE_SUGGESTION_SELECT,
      position: 3,
      completion: '@alice@example.com',
    }));
  });
});
