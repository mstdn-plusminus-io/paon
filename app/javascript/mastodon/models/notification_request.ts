import type { ApiNotificationRequestJSON } from 'mastodon/api_types/notifications';

export interface NotificationRequest {
  id: string;
  created_at: string;
  updated_at: string;
  notifications_count: number;
  account_id: string;
  last_status_id?: string;
}

export function createNotificationRequestFromJSON(
  json: ApiNotificationRequestJSON,
): NotificationRequest {
  return {
    id: json.id,
    created_at: json.created_at,
    updated_at: json.updated_at,
    notifications_count: Number.parseInt(json.notifications_count, 10) || 0,
    account_id: json.account.id,
    last_status_id: json.last_status?.id,
  };
}
