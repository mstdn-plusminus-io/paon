import { appendMenuItemWithSeparator } from '../menu';

describe('Mastodon 4.4 status menu sections', () => {
  it('does not append an empty separator when the pin action is unavailable', () => {
    const menu = [{ text: 'Bookmark' }];
    expect(appendMenuItemWithSeparator(menu, { text: 'Pin' }, false)).toEqual(menu);
    expect(appendMenuItemWithSeparator(menu, { text: 'Pin' }, true)).toEqual([
      ...menu,
      { text: 'Pin' },
      null,
    ]);
  });
});
