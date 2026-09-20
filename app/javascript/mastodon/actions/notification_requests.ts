import {
  apiAcceptNotificationRequest,
  apiAcceptNotificationRequests,
  apiDismissNotificationRequest,
  apiDismissNotificationRequests,
  apiFetchNotificationRequest,
  apiFetchNotificationRequests,
  apiFetchNotificationsForAccount,
  apiNotificationRequestsMerged,
} from 'mastodon/api/notifications';
import type { ApiNotificationJSON } from 'mastodon/api_types/notifications';
import type { NotificationRequestState } from 'mastodon/reducers/notification_requests';
import type { AppDispatch, GetState } from 'mastodon/store';

import { importFetchedAccounts, importFetchedStatuses } from './importer';
import { notificationsMerged } from './notification_groups';
import {
  decreasePendingNotificationRequests,
  fetchNotificationPolicy,
} from './notification_policies';

export const NOTIFICATION_REQUESTS_FETCH_REQUEST =
  'NOTIFICATION_REQUESTS_FETCH_REQUEST';
export const NOTIFICATION_REQUESTS_FETCH_SUCCESS =
  'NOTIFICATION_REQUESTS_FETCH_SUCCESS';
export const NOTIFICATION_REQUESTS_FETCH_FAIL =
  'NOTIFICATION_REQUESTS_FETCH_FAIL';
export const NOTIFICATION_REQUEST_FETCH_REQUEST =
  'NOTIFICATION_REQUEST_FETCH_REQUEST';
export const NOTIFICATION_REQUEST_FETCH_SUCCESS =
  'NOTIFICATION_REQUEST_FETCH_SUCCESS';
export const NOTIFICATION_REQUEST_FETCH_FAIL =
  'NOTIFICATION_REQUEST_FETCH_FAIL';
export const NOTIFICATION_REQUEST_NOTIFICATIONS_REQUEST =
  'NOTIFICATION_REQUEST_NOTIFICATIONS_REQUEST';
export const NOTIFICATION_REQUEST_NOTIFICATIONS_SUCCESS =
  'NOTIFICATION_REQUEST_NOTIFICATIONS_SUCCESS';
export const NOTIFICATION_REQUEST_NOTIFICATIONS_FAIL =
  'NOTIFICATION_REQUEST_NOTIFICATIONS_FAIL';
export const NOTIFICATION_REQUEST_MUTATE_REQUEST =
  'NOTIFICATION_REQUEST_MUTATE_REQUEST';
export const NOTIFICATION_REQUEST_MUTATE_SUCCESS =
  'NOTIFICATION_REQUEST_MUTATE_SUCCESS';
export const NOTIFICATION_REQUEST_MUTATE_FAIL =
  'NOTIFICATION_REQUEST_MUTATE_FAIL';

const importNotifications = (
  dispatch: AppDispatch,
  notifications: ApiNotificationJSON[],
) => {
  const accounts = notifications.flatMap((notification) => {
    const result = [];
    if (notification.account) result.push(notification.account);
    if (notification.report?.target_account)
      result.push(notification.report.target_account);
    if (notification.moderation_warning?.target_account)
      result.push(notification.moderation_warning.target_account);
    return result;
  });
  const statuses = notifications.flatMap((notification) =>
    notification.status ? [notification.status] : [],
  );
  if (accounts.length > 0) dispatch(importFetchedAccounts(accounts));
  if (statuses.length > 0) dispatch(importFetchedStatuses(statuses));
};

export const fetchNotificationRequests =
  (url?: string) => async (dispatch: AppDispatch, getState: GetState) => {
    if ((getState().notificationRequests as NotificationRequestState).isLoading)
      return;
    dispatch({
      type: NOTIFICATION_REQUESTS_FETCH_REQUEST,
      append: Boolean(url),
      skipLoading: Boolean(url),
    });
    try {
      const response = await apiFetchNotificationRequests(undefined, url);
      const statuses = response.data.flatMap((request) =>
        request.last_status ? [request.last_status] : [],
      );
      dispatch(
        importFetchedAccounts(response.data.map((request) => request.account)),
      );
      if (statuses.length > 0) dispatch(importFetchedStatuses(statuses));
      const next = response.links.refs.find((link) => link.rel === 'next')?.uri;
      dispatch({
        type: NOTIFICATION_REQUESTS_FETCH_SUCCESS,
        requests: response.data,
        next,
        append: Boolean(url),
        skipLoading: Boolean(url),
      });
    } catch (error) {
      dispatch({
        type: NOTIFICATION_REQUESTS_FETCH_FAIL,
        error,
        skipLoading: Boolean(url),
      });
    }
  };

export const expandNotificationRequests =
  () => (dispatch: AppDispatch, getState: GetState) => {
    const next = (getState().notificationRequests as NotificationRequestState)
      .next;
    if (next) void dispatch(fetchNotificationRequests(next));
  };

export const fetchNotificationRequest =
  (id: string) => async (dispatch: AppDispatch) => {
    dispatch({ type: NOTIFICATION_REQUEST_FETCH_REQUEST, id });
    try {
      const request = await apiFetchNotificationRequest(id);
      dispatch(importFetchedAccounts([request.account]));
      if (request.last_status)
        dispatch(importFetchedStatuses([request.last_status]));
      dispatch({ type: NOTIFICATION_REQUEST_FETCH_SUCCESS, request });
    } catch (error) {
      dispatch({
        type: NOTIFICATION_REQUEST_FETCH_FAIL,
        error,
        id,
        skipNotFound: true,
      });
    }
  };

export const fetchNotificationsForRequest =
  (accountId: string, url?: string) => async (dispatch: AppDispatch) => {
    dispatch({
      type: NOTIFICATION_REQUEST_NOTIFICATIONS_REQUEST,
      accountId,
      append: Boolean(url),
      skipLoading: Boolean(url),
    });
    try {
      const response = await apiFetchNotificationsForAccount(accountId, url);
      importNotifications(dispatch, response.data);
      const next = response.links.refs.find((link) => link.rel === 'next')?.uri;
      dispatch({
        type: NOTIFICATION_REQUEST_NOTIFICATIONS_SUCCESS,
        notifications: response.data,
        accountId,
        next,
        append: Boolean(url),
        skipLoading: Boolean(url),
      });
    } catch (error) {
      dispatch({
        type: NOTIFICATION_REQUEST_NOTIFICATIONS_FAIL,
        error,
        accountId,
        skipLoading: Boolean(url),
      });
    }
  };

export const expandNotificationsForRequest =
  (accountId: string) => (dispatch: AppDispatch, getState: GetState) => {
    const next = (getState().notificationRequests as NotificationRequestState)
      .current.notifications.next;
    if (next) void dispatch(fetchNotificationsForRequest(accountId, next));
  };

const waitUntilMerged = (dispatch: AppDispatch, attempts = 20): void => {
  void apiNotificationRequestsMerged()
    .then((merged) => {
      if (merged || attempts <= 1) {
        dispatch(notificationsMerged());
        void dispatch(fetchNotificationRequests());
        void dispatch(fetchNotificationPolicy());
        return;
      } else {
        window.setTimeout(() => {
          waitUntilMerged(dispatch, attempts - 1);
        }, 500);
        return;
      }
    })
    .catch(() => {
      dispatch(notificationsMerged());
    });
};

const mutateRequests =
  (ids: string[], accept: boolean) => async (dispatch: AppDispatch) => {
    const uniqueIds = Array.from(new Set(ids)).filter(Boolean);
    if (uniqueIds.length === 0) return;
    dispatch({
      type: NOTIFICATION_REQUEST_MUTATE_REQUEST,
      ids: uniqueIds,
      accept,
    });
    try {
      const onlyId = uniqueIds[0];
      if (uniqueIds.length === 1 && onlyId) {
        if (accept) await apiAcceptNotificationRequest(onlyId);
        else await apiDismissNotificationRequest(onlyId);
      } else if (accept) {
        await apiAcceptNotificationRequests(uniqueIds);
      } else {
        await apiDismissNotificationRequests(uniqueIds);
      }
      dispatch({
        type: NOTIFICATION_REQUEST_MUTATE_SUCCESS,
        ids: uniqueIds,
        accept,
      });
      dispatch(decreasePendingNotificationRequests(uniqueIds.length));
      if (accept) waitUntilMerged(dispatch);
      else void dispatch(fetchNotificationPolicy());
    } catch (error) {
      dispatch({
        type: NOTIFICATION_REQUEST_MUTATE_FAIL,
        ids: uniqueIds,
        error,
        accept,
      });
      void dispatch(fetchNotificationRequests());
    }
  };

export const acceptNotificationRequest = (id: string) =>
  mutateRequests([id], true);
export const dismissNotificationRequest = (id: string) =>
  mutateRequests([id], false);
export const acceptNotificationRequests = (ids: string[]) =>
  mutateRequests(ids, true);
export const dismissNotificationRequests = (ids: string[]) =>
  mutateRequests(ids, false);
