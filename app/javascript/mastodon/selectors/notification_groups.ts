import type { Map as ImmutableMap } from 'immutable';

import { compareId } from 'mastodon/compare_id';
import type { NotificationGroup } from 'mastodon/models/notification_group';
import type {
  NotificationGap,
  NotificationGroupsState,
} from 'mastodon/reducers/notification_groups';
import type { RootState } from 'mastodon/store';

const allowedGroups = (
  state: RootState,
  groups: (NotificationGroup | NotificationGap)[],
) => {
  const showFilterBar =
    (state.getIn(['settings', 'notifications', 'quickFilter', 'show']) as
      | boolean
      | undefined) ?? true;
  const active =
    (state.getIn(['settings', 'notifications', 'quickFilter', 'active']) as
      | string
      | undefined) ?? 'all';
  const shows = state.getIn(['settings', 'notifications', 'shows']) as
    | ImmutableMap<string, boolean>
    | undefined;

  return groups.filter((item) => {
    if (item.kind === 'gap') return true;
    if (showFilterBar && active !== 'all') return item.type === active;
    return shows?.get(item.type, true) ?? true;
  });
};

const notificationGroupsState = (state: RootState) =>
  state.notificationGroups as NotificationGroupsState;

export const selectNotificationGroups = (state: RootState) =>
  allowedGroups(state, notificationGroupsState(state).groups);

export const selectPendingNotificationGroups = (state: RootState) =>
  allowedGroups(state, notificationGroupsState(state).pendingGroups);

export const selectPendingNotificationGroupsCount = (state: RootState) =>
  selectPendingNotificationGroups(state).filter((item) => item.kind !== 'gap')
    .length;

export const selectUnreadNotificationGroupsCount = (state: RootState) => {
  const marker = notificationGroupsState(state).lastReadId;
  return [
    ...selectNotificationGroups(state),
    ...selectPendingNotificationGroups(state),
  ].filter(
    (item) =>
      item.kind === 'notification' &&
      item.page_max_id &&
      compareId(item.page_max_id, marker) > 0,
  ).length;
};

export const selectAnyUnreadNotificationGroup = (state: RootState) => {
  const marker = notificationGroupsState(state).readMarkerId;
  return selectNotificationGroups(state).some(
    (item) =>
      item.kind === 'notification' &&
      item.page_max_id &&
      compareId(item.page_max_id, marker) > 0,
  );
};

export const selectNotificationGroupsState = (
  state: RootState,
): NotificationGroupsState => notificationGroupsState(state);
