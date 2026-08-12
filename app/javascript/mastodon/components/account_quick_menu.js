export const buildAccountQuickMenu = ({ defaultAction, accountUrl, mutingNotifications, labels, actions }) => {
  if (defaultAction === 'block') {
    return [];
  }

  if (defaultAction === 'mute') {
    return [{
      text: mutingNotifications ? labels.unmuteNotifications : labels.muteNotifications,
      action: mutingNotifications ? actions.unmuteNotifications : actions.muteNotifications,
    }];
  }

  const menu = [{ text: labels.addToLists, action: actions.addToLists }];

  if (accountUrl) {
    menu.unshift({ text: labels.openOriginalPage, href: accountUrl }, null);
  }

  return menu;
};
