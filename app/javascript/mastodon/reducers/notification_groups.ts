import type { UnknownAction } from '@reduxjs/toolkit';

import type {
  ApiNotificationGroupJSON,
  ApiNotificationJSON,
} from 'mastodon/api_types/notifications';
import { compareId } from 'mastodon/compare_id';
import {
  NOTIFICATIONS_GROUP_MAX_AVATARS,
  createNotificationGroupFromJSON,
  createNotificationGroupFromNotificationJSON,
} from 'mastodon/models/notification_group';
import type { NotificationGroup } from 'mastodon/models/notification_group';

import { MARKERS_FETCH_SUCCESS } from '../actions/markers';
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
} from '../actions/notification_group_types';

export interface NotificationGap {
  kind: 'gap';
  type: 'gap';
  maxId?: string;
  sinceId?: string;
  url?: string;
}

export interface NotificationGroupsState {
  groups: (NotificationGroup | NotificationGap)[];
  pendingGroups: NotificationGroup[];
  isLoading: boolean;
  scrolledToTop: boolean;
  mounted: number;
  lastReadId: string;
  readMarkerId: string;
  isTabVisible: boolean;
  mergedNotifications: 'ok' | 'needs-reload';
}

const initialState: NotificationGroupsState = {
  groups: [],
  pendingGroups: [],
  isLoading: false,
  scrolledToTop: false,
  mounted: 0,
  lastReadId: '0',
  readMarkerId: '0',
  isTabVisible: true,
  mergedNotifications: 'ok',
};

const isGroup = (
  item: NotificationGroup | NotificationGap,
): item is NotificationGroup => item.kind === 'notification';

const mergeGroup = (
  current: NotificationGroup,
  incoming: NotificationGroup,
): NotificationGroup => {
  const sampleAccountIds = Array.from(
    new Set([...incoming.sampleAccountIds, ...current.sampleAccountIds]),
  ).slice(0, NOTIFICATIONS_GROUP_MAX_AVATARS);
  const incomingIsNewer =
    compareId(
      incoming.most_recent_notification_id,
      current.most_recent_notification_id,
    ) >= 0;
  const newer = incomingIsNewer ? incoming : current;
  const older = incomingIsNewer ? current : incoming;

  return {
    ...older,
    ...newer,
    notifications_count: Math.max(
      current.notifications_count,
      incoming.notifications_count,
    ),
    sampleAccountIds,
    page_min_id:
      current.page_min_id && incoming.page_min_id
        ? compareId(current.page_min_id, incoming.page_min_id) <= 0
          ? current.page_min_id
          : incoming.page_min_id
        : current.page_min_id ?? incoming.page_min_id,
    page_max_id:
      current.page_max_id && incoming.page_max_id
        ? compareId(current.page_max_id, incoming.page_max_id) >= 0
          ? current.page_max_id
          : incoming.page_max_id
        : current.page_max_id ?? incoming.page_max_id,
    partial: current.partial && incoming.partial,
  };
};

const collapseGaps = (items: (NotificationGroup | NotificationGap)[]) => {
  const result: (NotificationGroup | NotificationGap)[] = [];
  items.forEach((item) => {
    const previous = result.at(-1);
    if (item.kind === 'gap' && previous?.kind === 'gap') {
      result[result.length - 1] = {
        kind: 'gap',
        type: 'gap',
        maxId: previous.maxId ?? item.maxId,
        sinceId: item.sinceId ?? previous.sinceId,
        url: previous.url ?? item.url,
      };
    } else {
      result.push(item);
    }
  });
  return result;
};

const replaceGroups = (json: ApiNotificationGroupJSON[], next?: string) => {
  const groups: (NotificationGroup | NotificationGap)[] = json.map(
    createNotificationGroupFromJSON,
  );
  if (next) {
    groups.push({
      kind: 'gap',
      type: 'gap',
      maxId: json.at(-1)?.page_min_id,
      url: next,
    });
  }
  return groups;
};

const fillGap = (
  current: (NotificationGroup | NotificationGap)[],
  gap: NotificationGap,
  json: ApiNotificationGroupJSON[],
  next?: string,
) => {
  const gapIndex = current.findIndex(
    (item) =>
      item === gap ||
      (item.kind === 'gap' &&
        item.url === gap.url &&
        item.maxId === gap.maxId &&
        item.sinceId === gap.sinceId),
  );
  if (gapIndex < 0) return current;

  const before = current.slice(0, gapIndex);
  const after = current.slice(gapIndex + 1);
  const inserted: NotificationGroup[] = [];

  json.map(createNotificationGroupFromJSON).forEach((incoming) => {
    const beforeIndex = before.findIndex(
      (item) => isGroup(item) && item.group_key === incoming.group_key,
    );
    if (beforeIndex >= 0) {
      const existing = before[beforeIndex];
      if (existing && isGroup(existing))
        before[beforeIndex] = mergeGroup(existing, incoming);
      return;
    }

    const afterIndex = after.findIndex(
      (item) => isGroup(item) && item.group_key === incoming.group_key,
    );
    if (afterIndex >= 0) {
      const existing = after[afterIndex];
      if (existing && isGroup(existing))
        inserted.push(mergeGroup(existing, incoming));
      after.splice(afterIndex, 1);
      return;
    }

    inserted.push(incoming);
  });

  const replacement: (NotificationGroup | NotificationGap)[] = [...inserted];
  if (next)
    replacement.push({
      kind: 'gap',
      type: 'gap',
      maxId: json.at(-1)?.page_min_id,
      sinceId: gap.sinceId,
      url: next,
    });

  return collapseGaps([...before, ...replacement, ...after]);
};

const mergeRecent = (
  current: (NotificationGroup | NotificationGap)[],
  json: ApiNotificationGroupJSON[],
) => {
  const groups = [...current];
  const fresh: NotificationGroup[] = [];
  json.map(createNotificationGroupFromJSON).forEach((incoming) => {
    const index = groups.findIndex(
      (item) => isGroup(item) && item.group_key === incoming.group_key,
    );
    if (index >= 0) {
      const existing = groups[index];
      if (existing && isGroup(existing))
        fresh.push(mergeGroup(existing, incoming));
      groups.splice(index, 1);
    } else {
      fresh.push(incoming);
    }
  });
  return collapseGaps([...fresh, ...groups]);
};

const addStreamingNotification = (
  groups: NotificationGroup[],
  notification: ApiNotificationJSON,
  groupedTypes: string[],
) => {
  const normalized = createNotificationGroupFromNotificationJSON({
    ...notification,
    group_key: groupedTypes.includes(notification.type)
      ? notification.group_key
      : `ungrouped-${notification.id}`,
  });
  const index = groups.findIndex(
    (group) => group.group_key === normalized.group_key,
  );
  if (index < 0) return [normalized, ...groups];

  const existing = groups[index];
  if (!existing) return groups;
  const accountId = normalized.sampleAccountIds[0];
  const alreadySampled = accountId
    ? existing.sampleAccountIds.includes(accountId)
    : true;
  const merged = mergeGroup(existing, normalized);
  merged.notifications_count =
    existing.notifications_count + (alreadySampled ? 0 : 1);
  const rest = groups.filter((_, groupIndex) => groupIndex !== index);
  return [merged, ...rest];
};

const newestGroup = (state: NotificationGroupsState) =>
  state.groups.find(isGroup);

const shouldMarkNewNotificationsAsRead = (state: NotificationGroupsState) => {
  const oldestGroup = [...state.groups].reverse().find(isGroup);
  const hasMore = state.groups.at(-1)?.kind === 'gap';
  const oldestGroupReached =
    !hasMore ||
    state.lastReadId === '0' ||
    Boolean(
      oldestGroup?.page_min_id &&
        compareId(oldestGroup.page_min_id, state.lastReadId) <= 0,
    );

  return (
    state.isTabVisible &&
    state.scrolledToTop &&
    state.mounted > 0 &&
    oldestGroupReached
  );
};

const updateReadMarker = (state: NotificationGroupsState) => {
  const newest = newestGroup(state);
  if (
    newest?.page_max_id &&
    shouldMarkNewNotificationsAsRead(state) &&
    compareId(newest.page_max_id, state.lastReadId) > 0
  ) {
    state.lastReadId = newest.page_max_id;
  }
};

const commitReadMarker = (state: NotificationGroupsState) => {
  if (shouldMarkNewNotificationsAsRead(state)) {
    state.readMarkerId = state.lastReadId;
  }
};

interface NotificationGroupsAction extends UnknownAction {
  mode?: 'replace' | 'gap' | 'recent';
  groups?: ApiNotificationGroupJSON[];
  next?: string;
  gap?: NotificationGap;
  usePendingItems?: boolean;
  notification?: ApiNotificationJSON;
  groupedTypes?: string[];
  top?: boolean;
  markers?: { notifications?: { last_read_id?: string } };
  groupKey?: string;
  deferred?: boolean;
}

export default function notificationGroups(
  state: NotificationGroupsState = initialState,
  action: NotificationGroupsAction,
): NotificationGroupsState {
  switch (action.type) {
    case NOTIFICATION_GROUPS_FETCH_REQUEST:
      return { ...state, isLoading: true };
    case NOTIFICATION_GROUPS_FETCH_FAIL:
      return { ...state, isLoading: false };
    case NOTIFICATION_GROUPS_FETCH_SUCCESS: {
      const payloadGroups = action.groups ?? [];
      const groups =
        action.mode === 'replace'
          ? replaceGroups(payloadGroups, action.next)
          : action.mode === 'recent'
          ? mergeRecent(state.groups, payloadGroups)
          : action.gap
          ? fillGap(state.groups, action.gap, payloadGroups, action.next)
          : state.groups;
      const nextState = {
        ...state,
        groups,
        isLoading: false,
        mergedNotifications: 'ok' as const,
      };
      updateReadMarker(nextState);
      return nextState;
    }
    case NOTIFICATION_GROUPS_PROCESS_NEW: {
      if (!action.notification) return state;
      const groupedTypes = action.groupedTypes ?? [];
      if (action.usePendingItems || state.pendingGroups.length > 0) {
        return {
          ...state,
          pendingGroups: addStreamingNotification(
            state.pendingGroups,
            action.notification,
            groupedTypes,
          ),
        };
      }
      const existingGroups = state.groups.filter(isGroup);
      const gaps = state.groups.filter(
        (item): item is NotificationGap => item.kind === 'gap',
      );
      const groups = [
        ...addStreamingNotification(
          existingGroups,
          action.notification,
          groupedTypes,
        ),
        ...gaps,
      ];
      const nextState = { ...state, groups };
      updateReadMarker(nextState);
      return nextState;
    }
    case NOTIFICATION_GROUPS_LOAD_PENDING: {
      let groups = [...state.groups];
      [...state.pendingGroups].reverse().forEach((pending) => {
        groups = mergeRecent(groups, [
          {
            group_key: pending.group_key,
            notifications_count: pending.notifications_count,
            type: pending.type,
            sample_account_ids: pending.sampleAccountIds,
            most_recent_notification_id: pending.most_recent_notification_id,
            page_min_id: pending.page_min_id,
            page_max_id: pending.page_max_id,
            latest_page_notification_at: pending.latest_page_notification_at,
            status_id: pending.statusId,
            report: pending.report,
            event: pending.event,
            moderation_warning: pending.moderationWarning,
          },
        ]);
      });
      return { ...state, groups, pendingGroups: [] };
    }
    case NOTIFICATION_GROUPS_SCROLL: {
      const nextState = { ...state, scrolledToTop: Boolean(action.top) };
      if (action.top && state.mergedNotifications === 'needs-reload')
        return nextState;
      updateReadMarker(nextState);
      return nextState;
    }
    case NOTIFICATION_GROUPS_MARK_READ: {
      const newest = newestGroup(state);
      const id = newest?.page_max_id ?? state.lastReadId;
      return { ...state, lastReadId: id, readMarkerId: id };
    }
    case NOTIFICATION_GROUPS_MOUNT: {
      const nextState = { ...state, mounted: state.mounted + 1 };
      commitReadMarker(nextState);
      updateReadMarker(nextState);
      return nextState;
    }
    case NOTIFICATION_GROUPS_UNMOUNT:
      return { ...state, mounted: Math.max(0, state.mounted - 1) };
    case 'APP_FOCUS': {
      const nextState = {
        ...state,
        isTabVisible: true,
      };
      commitReadMarker(nextState);
      updateReadMarker(nextState);
      return nextState;
    }
    case 'APP_UNFOCUS':
      return { ...state, isTabVisible: false };
    case MARKERS_FETCH_SUCCESS: {
      const marker = action.markers?.notifications?.last_read_id;
      if (!marker || compareId(marker, state.lastReadId) <= 0) return state;
      return { ...state, lastReadId: marker, readMarkerId: marker };
    }
    case NOTIFICATION_GROUPS_CLEAR_LOCAL:
      return { ...state, groups: [], pendingGroups: [] };
    case NOTIFICATION_GROUP_DISMISS_LOCAL:
      return {
        ...state,
        groups: collapseGaps(
          state.groups.filter(
            (item) => item.kind === 'gap' || item.group_key !== action.groupKey,
          ),
        ),
        pendingGroups: state.pendingGroups.filter(
          (item) => item.group_key !== action.groupKey,
        ),
      };
    case NOTIFICATION_GROUPS_MERGED:
      return {
        ...state,
        mergedNotifications: action.deferred ? 'needs-reload' : 'ok',
      };
    default:
      return state;
  }
}
