import type { UnknownAction } from '@reduxjs/toolkit';

import type { NotificationPolicyJSON } from 'mastodon/api_types/notifications';

import {
  NOTIFICATION_POLICY_DECREASE_REQUESTS,
  NOTIFICATION_POLICY_FETCH_SUCCESS,
  NOTIFICATION_POLICY_UPDATE_REQUEST,
  NOTIFICATION_POLICY_UPDATE_SUCCESS,
} from '../actions/notification_policies';

interface NotificationPolicyAction extends UnknownAction {
  policy?: NotificationPolicyJSON;
  changes?: Partial<NotificationPolicyJSON>;
  count?: number;
}

export default function notificationPolicy(
  state: NotificationPolicyJSON | null = null,
  action: NotificationPolicyAction,
): NotificationPolicyJSON | null {
  switch (action.type) {
    case NOTIFICATION_POLICY_FETCH_SUCCESS:
    case NOTIFICATION_POLICY_UPDATE_SUCCESS:
      return action.policy ?? state;
    case NOTIFICATION_POLICY_UPDATE_REQUEST:
      return state ? { ...state, ...(action.changes ?? {}) } : state;
    case NOTIFICATION_POLICY_DECREASE_REQUESTS:
      return state
        ? {
            ...state,
            summary: {
              ...state.summary,
              pending_requests_count: Math.max(
                0,
                state.summary.pending_requests_count - (action.count ?? 0),
              ),
            },
          }
        : state;
    default:
      return state;
  }
}
