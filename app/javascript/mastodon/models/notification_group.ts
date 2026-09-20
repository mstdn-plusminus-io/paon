import type {
  ApiAccountRelationshipSeveranceEventJSON,
  ApiAccountWarningJSON,
  ApiNotificationGroupJSON,
  ApiNotificationJSON,
  ApiReportJSON,
} from 'mastodon/api_types/notifications';

export const NOTIFICATIONS_GROUP_MAX_AVATARS = 8;

export interface NotificationGroup {
  kind: 'notification';
  group_key: string;
  notifications_count: number;
  type: string;
  sampleAccountIds: string[];
  most_recent_notification_id: string;
  page_min_id?: string;
  page_max_id?: string;
  latest_page_notification_at?: string;
  statusId?: string;
  report?: ApiReportJSON | null;
  event?: ApiAccountRelationshipSeveranceEventJSON | null;
  moderationWarning?: ApiAccountWarningJSON | null;
  partial: boolean;
}

export function createNotificationGroupFromJSON(
  json: ApiNotificationGroupJSON,
): NotificationGroup {
  return {
    kind: 'notification',
    group_key: json.group_key,
    notifications_count: json.notifications_count,
    type: json.type,
    sampleAccountIds: json.sample_account_ids,
    most_recent_notification_id: json.most_recent_notification_id,
    page_min_id: json.page_min_id,
    page_max_id: json.page_max_id,
    latest_page_notification_at: json.latest_page_notification_at,
    statusId: json.status_id ?? undefined,
    report: json.report,
    event: json.event,
    moderationWarning: json.moderation_warning,
    partial: false,
  };
}

export function createNotificationGroupFromNotificationJSON(
  notification: ApiNotificationJSON,
): NotificationGroup {
  const accountId = notification.account?.id;

  return {
    kind: 'notification',
    group_key: notification.group_key?.trim()
      ? notification.group_key
      : `ungrouped-${notification.id}`,
    notifications_count: 1,
    type: notification.type,
    sampleAccountIds: accountId ? [accountId] : [],
    most_recent_notification_id: notification.id,
    page_min_id: notification.id,
    page_max_id: notification.id,
    latest_page_notification_at: notification.created_at,
    statusId: notification.status?.id,
    report: notification.report,
    event: notification.event,
    moderationWarning: notification.moderation_warning,
    partial: true,
  };
}
