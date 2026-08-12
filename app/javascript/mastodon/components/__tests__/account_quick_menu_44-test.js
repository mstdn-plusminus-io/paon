import { buildAccountQuickMenu } from '../account_quick_menu';

const addToLists = jest.fn();
const muteNotifications = jest.fn();
const unmuteNotifications = jest.fn();
const labels = {
  addToLists: 'Add to lists',
  openOriginalPage: 'Open original page',
  muteNotifications: 'Mute notifications',
  unmuteNotifications: 'Unmute notifications',
};
const actions = { addToLists, muteNotifications, unmuteNotifications };

describe('Mastodon 4.4 account quick menu', () => {
  it('offers the original profile and list editor in normal account lists', () => {
    expect(buildAccountQuickMenu({ defaultAction: undefined, accountUrl: 'https://remote.test/@alice', labels, actions })).toEqual([
      { text: 'Open original page', href: 'https://remote.test/@alice' },
      null,
      { text: 'Add to lists', action: addToLists },
    ]);
  });

  it('uses the quick menu for notification muting and omits it from block lists', () => {
    expect(buildAccountQuickMenu({ defaultAction: 'mute', mutingNotifications: true, labels, actions })).toEqual([
      { text: 'Unmute notifications', action: unmuteNotifications },
    ]);
    expect(buildAccountQuickMenu({ defaultAction: 'block', labels, actions })).toEqual([]);
  });
});
