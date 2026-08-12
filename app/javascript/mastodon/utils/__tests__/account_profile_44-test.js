import { fromJS } from 'immutable';

import {
  accountRelationshipTagKeys,
  PROFILE_AVATAR_SIZE,
  shouldShowFamiliarFollowers,
} from '../account_profile';

describe('Mastodon 4.4 account profile compatibility', () => {
  it('keeps the profile avatar at twice the post avatar size', () => {
    expect(PROFILE_AVATAR_SIZE).toBe(92);
  });

  it('does not show familiar followers for signed-out, self, suspended, or limited profiles', () => {
    const visible = {
      accountId: '2',
      currentAccountId: '1',
      signedIn: true,
      suspended: false,
      hidden: false,
    };

    expect(shouldShowFamiliarFollowers(visible)).toBe(true);
    expect(shouldShowFamiliarFollowers({ ...visible, signedIn: false })).toBe(false);
    expect(shouldShowFamiliarFollowers({ ...visible, accountId: '1' })).toBe(false);
    expect(shouldShowFamiliarFollowers({ ...visible, suspended: true })).toBe(false);
    expect(shouldShowFamiliarFollowers({ ...visible, hidden: true })).toBe(false);
  });

  it('uses the 4.4 relationship tags and allows moderation tags together', () => {
    const relationship = fromJS({
      followed_by: true,
      requested: true,
      requested_by: true,
      blocking: true,
      muting: true,
      domain_blocking: true,
    });

    expect(accountRelationshipTagKeys(relationship)).toEqual([
      'mutual',
      'blocking',
      'muting',
      'domain_blocking',
    ]);
    expect(accountRelationshipTagKeys(fromJS({ requested_by: true }))).toEqual(['requested_by']);
    expect(accountRelationshipTagKeys(relationship, true)).toEqual([]);
    expect(accountRelationshipTagKeys(null)).toEqual([]);
  });
});
