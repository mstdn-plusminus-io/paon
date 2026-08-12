export const allNotificationTypes = [
  'follow',
  'follow_request',
  'favourite',
  'reblog',
  'mention',
  'poll',
  'status',
  'update',
  'admin.sign_up',
  'admin.report',
  'moderation_warning',
  'severed_relationships',
] as const;

export type NotificationType = (typeof allNotificationTypes)[number];

export type NotificationPolicyValue = 'accept' | 'filter' | 'drop';

export interface ApiAccountJSON {
  id: string;
  acct: string;
  username: string;
  display_name: string;
  avatar: string;
  avatar_static: string;
  limited?: boolean;
  [key: string]: unknown;
}

export interface ApiStatusJSON {
  id: string;
  account: ApiAccountJSON;
  filtered?: {
    filter: {
      context: string[];
      filter_action: string;
    };
  }[];
  [key: string]: unknown;
}

export interface ApiReportJSON {
  id: string;
  target_account: ApiAccountJSON | null;
  [key: string]: unknown;
}

export interface ApiAccountWarningJSON {
  id: string;
  action: string;
  text: string;
  status_ids: string[];
  created_at: string;
  target_account: ApiAccountJSON | null;
  appeal: unknown;
}

export interface ApiAccountRelationshipSeveranceEventJSON {
  id: string;
  type: string;
  purged: boolean;
  target_name: string;
  followers_count: number;
  following_count: number;
  created_at: string;
}

export interface ApiNotificationJSON {
  id: string;
  type: string;
  created_at: string;
  group_key?: string;
  filtered?: boolean;
  account: ApiAccountJSON | null;
  status?: ApiStatusJSON | null;
  report?: ApiReportJSON | null;
  event?: ApiAccountRelationshipSeveranceEventJSON | null;
  moderation_warning?: ApiAccountWarningJSON | null;
}

export interface ApiNotificationGroupJSON {
  group_key: string;
  notifications_count: number;
  type: string;
  sample_account_ids: string[];
  most_recent_notification_id: string;
  page_min_id?: string;
  page_max_id?: string;
  latest_page_notification_at?: string;
  status_id?: string | null;
  report?: ApiReportJSON | null;
  event?: ApiAccountRelationshipSeveranceEventJSON | null;
  moderation_warning?: ApiAccountWarningJSON | null;
}

export interface ApiNotificationGroupsResultJSON {
  accounts?: ApiAccountJSON[];
  partial_accounts?: ApiAccountJSON[];
  statuses: ApiStatusJSON[];
  notification_groups: ApiNotificationGroupJSON[];
}

export interface ApiNotificationRequestJSON {
  id: string;
  created_at: string;
  updated_at: string;
  notifications_count: string;
  account: ApiAccountJSON;
  last_status: ApiStatusJSON | null;
}

export interface NotificationPolicyJSON {
  for_not_following: NotificationPolicyValue;
  for_not_followers: NotificationPolicyValue;
  for_new_accounts: NotificationPolicyValue;
  for_private_mentions: NotificationPolicyValue;
  for_limited_accounts: NotificationPolicyValue;
  summary: {
    pending_requests_count: number;
    pending_notifications_count: number;
  };
}
