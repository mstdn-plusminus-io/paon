import { createIntl } from 'react-intl';

import { bookmarkActionTitle, favouriteActionTitle } from '../status_action_titles';

const intl = createIntl({ locale: 'en' });

describe('Mastodon 4.4 active status action titles', () => {
  it('describes the inverse favorite action when active', () => {
    expect(favouriteActionTitle(intl, false)).toBe('Favorite');
    expect(favouriteActionTitle(intl, true)).toBe('Remove from favorites');
  });

  it('describes the inverse bookmark action when active', () => {
    expect(bookmarkActionTitle(intl, false)).toBe('Bookmark');
    expect(bookmarkActionTitle(intl, true)).toBe('Remove bookmark');
  });
});
