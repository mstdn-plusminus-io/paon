import {
  apiGetNotificationPolicy,
  apiUpdateNotificationPolicy,
} from 'mastodon/api/notification_policies';
import type { NotificationPolicyJSON } from 'mastodon/api_types/notifications';
import type { AppDispatch } from 'mastodon/store';

export const NOTIFICATION_POLICY_FETCH_REQUEST =
  'NOTIFICATION_POLICY_FETCH_REQUEST';
export const NOTIFICATION_POLICY_FETCH_SUCCESS =
  'NOTIFICATION_POLICY_FETCH_SUCCESS';
export const NOTIFICATION_POLICY_FETCH_FAIL = 'NOTIFICATION_POLICY_FETCH_FAIL';
export const NOTIFICATION_POLICY_UPDATE_REQUEST =
  'NOTIFICATION_POLICY_UPDATE_REQUEST';
export const NOTIFICATION_POLICY_UPDATE_SUCCESS =
  'NOTIFICATION_POLICY_UPDATE_SUCCESS';
export const NOTIFICATION_POLICY_UPDATE_FAIL =
  'NOTIFICATION_POLICY_UPDATE_FAIL';
export const NOTIFICATION_POLICY_DECREASE_REQUESTS =
  'NOTIFICATION_POLICY_DECREASE_REQUESTS';

export const fetchNotificationPolicy = () => async (dispatch: AppDispatch) => {
  dispatch({ type: NOTIFICATION_POLICY_FETCH_REQUEST, skipLoading: true });
  try {
    const policy = await apiGetNotificationPolicy();
    dispatch({
      type: NOTIFICATION_POLICY_FETCH_SUCCESS,
      policy,
      skipLoading: true,
    });
  } catch (error) {
    dispatch({
      type: NOTIFICATION_POLICY_FETCH_FAIL,
      error,
      skipLoading: true,
      skipAlert: true,
    });
  }
};

export const updateNotificationPolicy =
  (changes: Partial<NotificationPolicyJSON>) =>
  async (dispatch: AppDispatch) => {
    dispatch({ type: NOTIFICATION_POLICY_UPDATE_REQUEST, changes });
    try {
      const policy = await apiUpdateNotificationPolicy(changes);
      dispatch({ type: NOTIFICATION_POLICY_UPDATE_SUCCESS, policy });
    } catch (error) {
      dispatch({ type: NOTIFICATION_POLICY_UPDATE_FAIL, error });
      void dispatch(fetchNotificationPolicy());
    }
  };

export const decreasePendingNotificationRequests = (count: number) => ({
  type: NOTIFICATION_POLICY_DECREASE_REQUESTS,
  count,
});
