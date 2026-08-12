import type { Map as ImmutableMap } from 'immutable';

import {
  apiClearNotificationGroups,
  apiDismissNotificationGroup,
  apiFetchNotificationGroups,
} from 'mastodon/api/notifications';
import type { ApiNotificationJSON } from 'mastodon/api_types/notifications';
import { allNotificationTypes } from 'mastodon/api_types/notifications';
import { usePendingItems } from 'mastodon/initial_state';
import type {
  NotificationGap,
  NotificationGroupsState,
} from 'mastodon/reducers/notification_groups';
import type { AppDispatch, GetState } from 'mastodon/store';

import { importFetchedAccounts, importFetchedStatuses } from './importer';
import {
  NOTIFICATION_GROUP_DISMISS_LOCAL,
  NOTIFICATION_GROUPS_CLEAR_LOCAL,
  NOTIFICATION_GROUPS_FETCH_FAIL,
  NOTIFICATION_GROUPS_FETCH_REQUEST,
  NOTIFICATION_GROUPS_FETCH_SUCCESS,
  NOTIFICATION_GROUPS_LOAD_PENDING,
  NOTIFICATION_GROUPS_MARK_READ,
  NOTIFICATION_GROUPS_MERGED,
  NOTIFICATION_GROUPS_MOUNT,
  NOTIFICATION_GROUPS_PROCESS_NEW,
  NOTIFICATION_GROUPS_SCROLL,
  NOTIFICATION_GROUPS_UNMOUNT,
} from './notification_group_types';
import { NOTIFICATIONS_FILTER_SET } from './notifications';
import { saveSettings } from './settings';

export * from './notification_group_types';

const groupedTypes = (getState: GetState) => {
  const types = ['favourite', 'reblog'];
  if (
    (getState().getIn(['settings', 'notifications', 'groupFollows']) as
      | boolean
      | undefined) ??
    true
  ) {
    types.push('follow');
  }
  return types;
};

const excludedTypes = (getState: GetState) => {
  const active =
    (getState().getIn([
      'settings',
      'notifications',
      'quickFilter',
      'active',
    ]) as string | undefined) ?? 'all';
  if (active !== 'all') {
    return allNotificationTypes.filter((type) => type !== active);
  }

  const shows = getState().getIn(['settings', 'notifications', 'shows']) as
    | ImmutableMap<string, boolean>
    | undefined;
  return shows
    ? shows
        .filter((enabled: boolean) => !enabled)
        .keySeq()
        .toArray()
    : [];
};

const importEnvelope = (
  dispatch: AppDispatch,
  data: Awaited<ReturnType<typeof apiFetchNotificationGroups>>['data'],
) => {
  const accounts = [...(data.accounts ?? []), ...(data.partial_accounts ?? [])];
  const extraAccounts = data.notification_groups.flatMap((group) => {
    if (group.report?.target_account) return [group.report.target_account];
    if (group.moderation_warning?.target_account)
      return [group.moderation_warning.target_account];
    return [];
  });

  if (accounts.length + extraAccounts.length > 0) {
    dispatch(importFetchedAccounts([...accounts, ...extraAccounts]));
  }
  if (data.statuses.length > 0) dispatch(importFetchedStatuses(data.statuses));
};

type FetchMode = 'replace' | 'gap' | 'recent';

const fetchGroups =
  (mode: FetchMode, gap?: NotificationGap) =>
  async (dispatch: AppDispatch, getState: GetState) => {
    dispatch({
      type: NOTIFICATION_GROUPS_FETCH_REQUEST,
      mode,
      gap,
      skipLoading: mode !== 'gap',
    });

    try {
      const state = getState().notificationGroups as NotificationGroupsState;
      const firstItem = state.groups.find(
        (item) => item.kind === 'notification',
      );
      const sinceId =
        mode === 'recent' && firstItem?.kind === 'notification'
          ? firstItem.page_max_id
          : undefined;
      const response = await apiFetchNotificationGroups(
        gap?.url
          ? undefined
          : {
              grouped_types: groupedTypes(getState),
              exclude_types: excludedTypes(getState),
              max_id: gap?.maxId,
              since_id: gap?.sinceId ?? sinceId,
              expand_accounts: 'full',
            },
        gap?.url,
      );
      importEnvelope(dispatch, response.data);
      const next = response.links.refs.find((link) => link.rel === 'next')?.uri;
      const prev = response.links.refs.find((link) => link.rel === 'prev')?.uri;

      dispatch({
        type: NOTIFICATION_GROUPS_FETCH_SUCCESS,
        mode,
        gap,
        groups: response.data.notification_groups,
        next,
        prev,
        skipLoading: mode !== 'gap',
      });
    } catch (error) {
      dispatch({
        type: NOTIFICATION_GROUPS_FETCH_FAIL,
        error,
        mode,
        gap,
        skipLoading: mode !== 'gap',
      });
    }
  };

export const fetchNotificationGroups = () => fetchGroups('replace');
export const fetchNotificationGroupsGap = (gap: NotificationGap) =>
  fetchGroups('gap', gap);
export const pollRecentNotificationGroups = () => fetchGroups('recent');

export const processNewNotificationForGroups =
  (notification: ApiNotificationJSON) =>
  (dispatch: AppDispatch, getState: GetState) => {
    const active =
      (getState().getIn([
        'settings',
        'notifications',
        'quickFilter',
        'active',
      ]) as string | undefined) ?? 'all';
    const shown =
      (getState().getIn([
        'settings',
        'notifications',
        'shows',
        notification.type,
      ]) as boolean | undefined) ?? true;
    if (
      (active !== 'all' && active !== notification.type) ||
      !shown ||
      notification.filtered
    )
      return;

    if (
      (notification.type === 'mention' || notification.type === 'update') &&
      notification.status?.filtered
    ) {
      const filters = notification.status.filtered.filter((result) =>
        result.filter.context.includes('notifications'),
      );
      if (filters.some((result) => result.filter.filter_action === 'hide'))
        return;
    }

    if (notification.account)
      dispatch(importFetchedAccounts([notification.account]));
    if (notification.status)
      dispatch(importFetchedStatuses([notification.status]));
    if (notification.report?.target_account)
      dispatch(importFetchedAccounts([notification.report.target_account]));
    if (notification.moderation_warning?.target_account)
      dispatch(
        importFetchedAccounts([notification.moderation_warning.target_account]),
      );

    dispatch({
      type: NOTIFICATION_GROUPS_PROCESS_NEW,
      notification,
      groupedTypes: groupedTypes(getState),
      usePendingItems: Boolean(usePendingItems),
    });
  };

export const loadPendingNotificationGroups = () => ({
  type: NOTIFICATION_GROUPS_LOAD_PENDING,
});
export const updateNotificationGroupsScroll =
  (top: boolean) => (dispatch: AppDispatch, getState: GetState) => {
    const needsReload =
      (getState().notificationGroups as NotificationGroupsState)
        .mergedNotifications === 'needs-reload';
    dispatch({ type: NOTIFICATION_GROUPS_SCROLL, top });
    if (top && needsReload) void dispatch(fetchNotificationGroups());
  };
export const markNotificationGroupsAsRead = () => ({
  type: NOTIFICATION_GROUPS_MARK_READ,
});
export const mountNotificationGroups = () => ({
  type: NOTIFICATION_GROUPS_MOUNT,
});
export const unmountNotificationGroups = () => ({
  type: NOTIFICATION_GROUPS_UNMOUNT,
});

export const setNotificationGroupsFilter =
  (filterType: string) => (dispatch: AppDispatch) => {
    dispatch({
      type: NOTIFICATIONS_FILTER_SET,
      path: ['notifications', 'quickFilter', 'active'],
      value: filterType,
    });
    void dispatch(fetchNotificationGroups());
    dispatch(saveSettings());
  };

export const clearNotificationGroups = () => async (dispatch: AppDispatch) => {
  dispatch({ type: NOTIFICATION_GROUPS_CLEAR_LOCAL });
  try {
    await apiClearNotificationGroups();
  } catch (error) {
    dispatch({ type: NOTIFICATION_GROUPS_FETCH_FAIL, error });
  }
};

export const dismissNotificationGroup =
  (groupKey: string) => async (dispatch: AppDispatch) => {
    dispatch({ type: NOTIFICATION_GROUP_DISMISS_LOCAL, groupKey });
    try {
      await apiDismissNotificationGroup(groupKey);
    } catch (error) {
      dispatch({ type: NOTIFICATION_GROUPS_FETCH_FAIL, error });
      void dispatch(fetchNotificationGroups());
    }
  };

export const notificationsMerged =
  () => (dispatch: AppDispatch, getState: GetState) => {
    const state = getState().notificationGroups as NotificationGroupsState;
    dispatch({
      type: NOTIFICATION_GROUPS_MERGED,
      deferred: state.mounted > 0 && !state.scrolledToTop,
    });
    if (state.mounted === 0 || state.scrolledToTop)
      void dispatch(fetchNotificationGroups());
  };
