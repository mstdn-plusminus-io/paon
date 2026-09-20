import api from 'mastodon/api';
import type { NotificationPolicyJSON } from 'mastodon/api_types/notifications';

export const apiGetNotificationPolicy = async () =>
  (await api().get<NotificationPolicyJSON>('/api/v2/notifications/policy'))
    .data;

export const apiUpdateNotificationPolicy = async (
  policy: Partial<NotificationPolicyJSON>,
) =>
  (
    await api().patch<NotificationPolicyJSON>(
      '/api/v2/notifications/policy',
      policy,
    )
  ).data;
