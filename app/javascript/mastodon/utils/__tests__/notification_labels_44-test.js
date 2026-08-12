import { fromJS } from 'immutable';

import { isPrivateMentionStatus } from '../notification_labels';

describe('Mastodon 4.4 notification labels', () => {
  it('identifies private mentions for favorite notification copy', () => {
    expect(isPrivateMentionStatus(fromJS({ visibility: 'direct' }))).toBe(true);
    expect(isPrivateMentionStatus(fromJS({ visibility: 'private' }))).toBe(false);
    expect(isPrivateMentionStatus(null)).toBe(false);
  });
});
