import { fromJS } from 'immutable';

import {
  COMPOSE_SPOILERNESS_CHANGE,
  COMPOSE_UPLOAD_SUCCESS,
  COMPOSE_UPLOAD_FAIL,
  COMPOSE_UPLOAD_PROGRESS,
  THUMBNAIL_UPLOAD_PROGRESS,
} from '../../actions/compose';
import { REDRAFT } from '../../actions/statuses';
import compose from '../compose';

describe('Mastodon 4.4 sensitive media compose behavior', () => {
  it('opens the media warning when default-sensitive media is first attached', () => {
    const initial = compose(undefined, {}).set('default_sensitive', true);
    const state = compose(initial, {
      type: COMPOSE_UPLOAD_SUCCESS,
      media: { id: '1', type: 'image' },
      file: new File(['image'], 'image.png', { type: 'image/png' }),
    });

    expect(state.get('sensitive')).toBe(true);
    expect(state.get('spoiler')).toBe(true);
  });

  it('allows the warning toggle to unmark attached media as sensitive', () => {
    const initial = compose(undefined, {}).merge({
      spoiler: true,
      sensitive: true,
      media_attachments: fromJS([{ id: '1', type: 'image' }]),
    });
    const state = compose(initial, { type: COMPOSE_SPOILERNESS_CHANGE });

    expect(state.get('spoiler')).toBe(false);
    expect(state.get('sensitive')).toBe(false);
  });

  it('restores a media-only sensitive warning when redrafting', () => {
    const status = fromJS({
      id: '1',
      in_reply_to_id: null,
      quote: null,
      visibility: 'public',
      media_attachments: [{ id: '2', type: 'image' }],
      sensitive: true,
      language: 'en',
      spoiler_text: '',
      poll: null,
    });
    const state = compose(undefined, {
      type: REDRAFT,
      status,
      raw_text: 'A post',
    });

    expect(state.get('spoiler')).toBe(true);
    expect(state.get('spoiler_text')).toBe('');
  });

  it('clamps upload progress and resets it when the upload finishes or fails', () => {
    let state = compose(undefined, { type: COMPOSE_UPLOAD_PROGRESS, loaded: 12, total: 10 });
    expect(state.get('progress')).toBe(100);

    state = compose(state, {
      type: COMPOSE_UPLOAD_SUCCESS,
      media: { id: '1', type: 'image' },
      file: new File(['image'], 'image.png', { type: 'image/png' }),
    });
    expect(state.get('progress')).toBe(0);

    state = compose(state.set('progress', 80), { type: COMPOSE_UPLOAD_FAIL });
    expect(state.get('progress')).toBe(0);
  });

  it('clamps thumbnail upload progress', () => {
    const state = compose(undefined, { type: THUMBNAIL_UPLOAD_PROGRESS, loaded: 11, total: 10 });
    expect(state.get('thumbnailProgress')).toBe(100);
  });
});
