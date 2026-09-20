import type { AxiosResponse } from 'axios';

import api, { getLinks } from 'mastodon/api';
import type {
  ApiNotificationGroupsResultJSON,
  ApiNotificationJSON,
  ApiNotificationRequestJSON,
} from 'mastodon/api_types/notifications';

export interface NotificationGroupsParams {
  grouped_types?: string[];
  exclude_types?: string[];
  max_id?: string;
  min_id?: string;
  since_id?: string;
  include_filtered?: boolean;
  expand_accounts?: 'full' | 'partial_avatars';
}

const resultWithLinks = <T>(response: AxiosResponse<T>) => ({
  data: response.data,
  links: getLinks(response),
});

export const apiFetchNotificationGroups = async (
  params?: NotificationGroupsParams,
  url?: string,
) =>
  resultWithLinks(
    await api().get<ApiNotificationGroupsResultJSON>(
      url ?? '/api/v2/notifications',
      { params },
    ),
  );

export const apiDismissNotificationGroup = (groupKey: string) =>
  api().post(`/api/v2/notifications/${encodeURIComponent(groupKey)}/dismiss`);

export const apiClearNotificationGroups = () =>
  api().post('/api/v2/notifications/clear');

export const apiFetchNotificationRequests = async (
  params?: { since_id?: string },
  url?: string,
) =>
  resultWithLinks(
    await api().get<ApiNotificationRequestJSON[]>(
      url ?? '/api/v1/notifications/requests',
      { params },
    ),
  );

export const apiFetchNotificationRequest = async (id: string) =>
  (
    await api().get<ApiNotificationRequestJSON>(
      `/api/v1/notifications/requests/${encodeURIComponent(id)}`,
    )
  ).data;

export const apiFetchNotificationsForAccount = async (
  accountId: string,
  url?: string,
) =>
  resultWithLinks(
    await api().get<ApiNotificationJSON[]>(url ?? '/api/v1/notifications', {
      params: url
        ? undefined
        : { account_id: accountId, include_filtered: true },
    }),
  );

export const apiAcceptNotificationRequest = (id: string) =>
  api().post(`/api/v1/notifications/requests/${encodeURIComponent(id)}/accept`);

export const apiDismissNotificationRequest = (id: string) =>
  api().post(
    `/api/v1/notifications/requests/${encodeURIComponent(id)}/dismiss`,
  );

export const apiAcceptNotificationRequests = (ids: string[]) =>
  api().post('/api/v1/notifications/requests/accept', { id: ids });

export const apiDismissNotificationRequests = (ids: string[]) =>
  api().post('/api/v1/notifications/requests/dismiss', { id: ids });

export const apiNotificationRequestsMerged = async () =>
  (
    await api().get<{ merged: boolean }>(
      '/api/v1/notifications/requests/merged',
    )
  ).data.merged;
