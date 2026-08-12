import { fromJS } from 'immutable';

import api from '../../api';
import tagsReducer from '../../reducers/tags';
import {
  featureHashtag,
  HASHTAG_FEATURE_REQUEST,
  HASHTAG_FEATURE_SUCCESS,
  HASHTAG_UNFEATURE_REQUEST,
  HASHTAG_UNFEATURE_SUCCESS,
  unfeatureHashtag,
} from '../tags';

jest.mock('../../api', () => ({
  __esModule: true,
  default: jest.fn(),
}));

describe('Mastodon 4.4 featured hashtag actions', () => {
  beforeEach(() => {
    jest.clearAllMocks();
  });

  it('uses the official feature and unfeature endpoints', async () => {
    const featured = { id: '7', name: 'paon', featuring: true, history: [] };
    const unfeatured = { ...featured, featuring: false };
    const post = jest.fn()
      .mockResolvedValueOnce({ data: featured })
      .mockResolvedValueOnce({ data: unfeatured });
    const dispatch = jest.fn();
    api.mockReturnValue({ post });

    await featureHashtag('paon')(dispatch);
    await unfeatureHashtag('paon')(dispatch);

    expect(post.mock.calls).toEqual([
      ['/api/v1/tags/paon/feature'],
      ['/api/v1/tags/paon/unfeature'],
    ]);
    expect(dispatch.mock.calls.map(([action]) => action.type)).toEqual([
      HASHTAG_FEATURE_REQUEST,
      HASHTAG_FEATURE_SUCCESS,
      HASHTAG_UNFEATURE_REQUEST,
      HASHTAG_UNFEATURE_SUCCESS,
    ]);
  });

  it('optimistically reflects featuring and replaces it with the API response', () => {
    const initial = fromJS({
      paon: { id: '7', name: 'paon', featuring: false, history: [] },
    });
    const requested = tagsReducer(initial, {
      type: HASHTAG_FEATURE_REQUEST,
      name: 'paon',
    });
    const featured = tagsReducer(requested, {
      type: HASHTAG_FEATURE_SUCCESS,
      name: 'paon',
      tag: { id: '7', name: 'Paon', featuring: true, history: [] },
    });
    const unfeatureRequested = tagsReducer(featured, {
      type: HASHTAG_UNFEATURE_REQUEST,
      name: 'paon',
    });

    expect(requested.getIn(['paon', 'featuring'])).toBe(true);
    expect(featured.getIn(['paon', 'name'])).toBe('Paon');
    expect(featured.getIn(['paon', 'featuring'])).toBe(true);
    expect(unfeatureRequested.getIn(['paon', 'featuring'])).toBe(false);
  });
});
