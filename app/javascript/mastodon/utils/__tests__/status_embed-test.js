import { fromJS } from 'immutable';

import { canEmbedStatus } from '../status_embed';

const status = (visibility, username, acct) => fromJS({
  visibility,
  account: { username, acct },
});

describe('canEmbedStatus', () => {
  it.each(['public', 'unlisted'])('allows local %s posts', visibility => {
    expect(canEmbedStatus(status(visibility, 'alice', 'alice'))).toBe(true);
  });

  it('hides the embed menu for remote posts, including signed-in views', () => {
    expect(canEmbedStatus(status('public', 'alice', 'alice@remote.example'))).toBe(false);
  });

  it.each(['private', 'direct'])('hides the embed menu for %s posts', visibility => {
    expect(canEmbedStatus(status(visibility, 'alice', 'alice'))).toBe(false);
  });
});

