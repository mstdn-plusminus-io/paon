import { fromJS } from 'immutable';
import type { Map as ImmutableMap } from 'immutable';

import type { UnknownAction } from '@reduxjs/toolkit';

import type {
  ApiNotificationJSON,
  ApiNotificationRequestJSON,
} from 'mastodon/api_types/notifications';
import { createNotificationRequestFromJSON } from 'mastodon/models/notification_request';
import type { NotificationRequest } from 'mastodon/models/notification_request';

import {
  NOTIFICATION_REQUESTS_FETCH_FAIL,
  NOTIFICATION_REQUESTS_FETCH_REQUEST,
  NOTIFICATION_REQUESTS_FETCH_SUCCESS,
  NOTIFICATION_REQUEST_FETCH_FAIL,
  NOTIFICATION_REQUEST_FETCH_REQUEST,
  NOTIFICATION_REQUEST_FETCH_SUCCESS,
  NOTIFICATION_REQUEST_MUTATE_REQUEST,
  NOTIFICATION_REQUEST_NOTIFICATIONS_FAIL,
  NOTIFICATION_REQUEST_NOTIFICATIONS_REQUEST,
  NOTIFICATION_REQUEST_NOTIFICATIONS_SUCCESS,
} from '../actions/notification_requests';

export interface NotificationRequestState {
  items: NotificationRequest[];
  isLoading: boolean;
  next: string | null;
  current: {
    item: NotificationRequest | null;
    isLoading: boolean;
    removed: boolean;
    notifications: {
      items: NotificationItem[];
      isLoading: boolean;
      next: string | null;
      accountId: string | null;
    };
  };
}

export type NotificationItem = ImmutableMap<string, unknown>;

const emptyCurrent = (): NotificationRequestState['current'] => ({
  item: null,
  isLoading: false,
  removed: false,
  notifications: { items: [], isLoading: false, next: null, accountId: null },
});

const initialState: NotificationRequestState = {
  items: [],
  isLoading: false,
  next: null,
  current: emptyCurrent(),
};

const notificationToMap = (notification: ApiNotificationJSON) =>
  fromJS({
    id: notification.id,
    type: notification.type,
    account: notification.account?.id,
    created_at: notification.created_at,
    status: notification.status?.id ?? null,
    report: notification.report ?? null,
    event: notification.event ?? null,
    moderation_warning: notification.moderation_warning ?? null,
    filtered: notification.filtered ?? false,
  }) as NotificationItem;

interface NotificationRequestsAction extends UnknownAction {
  requests?: ApiNotificationRequestJSON[];
  request?: ApiNotificationRequestJSON;
  notifications?: ApiNotificationJSON[];
  append?: boolean;
  next?: string;
  accountId?: string;
  ids?: string[];
}

export default function notificationRequests(
  state: NotificationRequestState = initialState,
  action: NotificationRequestsAction,
): NotificationRequestState {
  switch (action.type) {
    case NOTIFICATION_REQUESTS_FETCH_REQUEST:
      return { ...state, isLoading: true };
    case NOTIFICATION_REQUESTS_FETCH_FAIL:
      return { ...state, isLoading: false };
    case NOTIFICATION_REQUESTS_FETCH_SUCCESS: {
      const requests = (action.requests ?? []).map(
        createNotificationRequestFromJSON,
      );
      const existing = action.append ? state.items : [];
      const seen = new Set<string>();
      const items = [...existing, ...requests].filter((request) => {
        if (seen.has(request.id)) return false;
        seen.add(request.id);
        return true;
      });
      return { ...state, items, isLoading: false, next: action.next ?? null };
    }
    case NOTIFICATION_REQUEST_FETCH_REQUEST:
      return { ...state, current: { ...emptyCurrent(), isLoading: true } };
    case NOTIFICATION_REQUEST_FETCH_FAIL:
      return { ...state, current: { ...state.current, isLoading: false } };
    case NOTIFICATION_REQUEST_FETCH_SUCCESS:
      if (!action.request) return state;
      return {
        ...state,
        current: {
          ...state.current,
          item: createNotificationRequestFromJSON(action.request),
          isLoading: false,
        },
      };
    case NOTIFICATION_REQUEST_NOTIFICATIONS_REQUEST: {
      const changedAccount =
        state.current.notifications.accountId !== action.accountId;
      return {
        ...state,
        current: {
          ...state.current,
          notifications: changedAccount
            ? {
                items: [],
                isLoading: true,
                next: null,
                accountId: action.accountId ?? null,
              }
            : { ...state.current.notifications, isLoading: true },
        },
      };
    }
    case NOTIFICATION_REQUEST_NOTIFICATIONS_FAIL:
      return {
        ...state,
        current: {
          ...state.current,
          notifications: { ...state.current.notifications, isLoading: false },
        },
      };
    case NOTIFICATION_REQUEST_NOTIFICATIONS_SUCCESS: {
      if (state.current.notifications.accountId !== action.accountId)
        return state;
      const incoming = (action.notifications ?? []).map(notificationToMap);
      return {
        ...state,
        current: {
          ...state.current,
          notifications: {
            ...state.current.notifications,
            items: action.append
              ? [...state.current.notifications.items, ...incoming]
              : incoming,
            isLoading: false,
            next: action.next ?? null,
          },
        },
      };
    }
    case NOTIFICATION_REQUEST_MUTATE_REQUEST: {
      const ids = new Set<string>(action.ids ?? []);
      return {
        ...state,
        items: state.items.filter((item) => !ids.has(item.id)),
        current:
          state.current.item && ids.has(state.current.item.id)
            ? { ...state.current, removed: true }
            : state.current,
      };
    }
    default:
      return state;
  }
}
