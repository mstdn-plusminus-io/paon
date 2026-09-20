import { useEffect } from 'react';

import { FormattedMessage } from 'react-intl';

import { Link } from 'react-router-dom';

import { fetchNotificationPolicy } from 'mastodon/actions/notification_policies';
import type { NotificationPolicyJSON } from 'mastodon/api_types/notifications';
import { Icon } from 'mastodon/components/icon';
import { useAppDispatch, useAppSelector } from 'mastodon/store';

export const FilteredNotificationsBanner: React.FC = () => {
  const dispatch = useAppDispatch();
  const policy = useAppSelector(
    (state) => state.notificationPolicy as NotificationPolicyJSON | null,
  );
  const minimized = useAppSelector(
    (state) =>
      (state.getIn(['settings', 'notifications', 'minimizeFilteredBanner']) as
        | boolean
        | undefined) ?? false,
  );

  useEffect(() => {
    void dispatch(fetchNotificationPolicy());
    const interval = window.setInterval(() => {
      void dispatch(fetchNotificationPolicy());
    }, 120_000);
    return () => {
      window.clearInterval(interval);
    };
  }, [dispatch]);

  if (!policy || minimized || policy.summary.pending_requests_count <= 0)
    return null;

  return (
    <Link
      className='notification-43__filtered-banner'
      to='/notifications/requests'
    >
      <Icon id='archive' fixedWidth />
      <span>
        <strong>
          <FormattedMessage
            id='filtered_notifications_banner.title'
            defaultMessage='Filtered notifications'
          />
        </strong>
        <small>
          <FormattedMessage
            id='filtered_notifications_banner.pending_requests'
            defaultMessage='From {count, plural, =0 {no one} one {one person} other {# people}} you may know'
            values={{ count: policy.summary.pending_requests_count }}
          />
        </small>
      </span>
    </Link>
  );
};
