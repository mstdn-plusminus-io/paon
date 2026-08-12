/* eslint-disable react/jsx-no-bind */

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';

import { Link } from 'react-router-dom';

import type { Account } from '@/types/resources';
import {
  acceptNotificationRequest,
  dismissNotificationRequest,
} from 'mastodon/actions/notification_requests';
import { Avatar } from 'mastodon/components/avatar';
import { IconButton } from 'mastodon/components/icon_button';
import { useAppDispatch, useAppSelector } from 'mastodon/store';

const messages = defineMessages({
  accept: { id: 'notification_requests.accept', defaultMessage: 'Accept' },
  dismiss: {
    id: 'notification_requests.dismiss',
    defaultMessage: 'Dismiss',
  },
  select: {
    id: 'notification_requests.select_account',
    defaultMessage: 'Select @{acct}',
  },
});

export const NotificationRequestRow: React.FC<{
  id: string;
  accountId: string;
  notificationsCount: number;
  checked: boolean;
  selectionMode: boolean;
  onToggle: (id: string) => void;
}> = ({
  id,
  accountId,
  notificationsCount,
  checked,
  selectionMode,
  onToggle,
}) => {
  const dispatch = useAppDispatch();
  const intl = useIntl();
  const account = useAppSelector(
    (state) => state.getIn(['accounts', accountId]) as Account | undefined,
  );

  if (!account) return null;
  const displayName = account.get('display_name_html');

  return (
    <article className='notification-43__request'>
      {selectionMode && (
        <input
          type='checkbox'
          checked={checked}
          aria-label={intl.formatMessage(messages.select, {
            acct: account.get('acct'),
          })}
          onChange={() => {
            onToggle(id);
          }}
        />
      )}
      <Link
        to={`/notifications/requests/${id}`}
        className='notification-43__request-account'
      >
        <Avatar account={account} size={40} />
        <span>
          <strong
            dangerouslySetInnerHTML={{
              __html:
                displayName.trim().length > 0
                  ? displayName
                  : account.get('username'),
            }}
          />
          <small>
            @{account.get('acct')} ·{' '}
            <FormattedMessage
              id='notifications.group'
              defaultMessage='{count} notifications'
              values={{ count: notificationsCount }}
            />
          </small>
        </span>
      </Link>
      <div className='notification-43__request-actions'>
        <IconButton
          icon='check'
          title={intl.formatMessage(messages.accept)}
          onClick={() => {
            void dispatch(acceptNotificationRequest(id));
          }}
        />
        <IconButton
          icon='times'
          title={intl.formatMessage(messages.dismiss)}
          onClick={() => {
            void dispatch(dismissNotificationRequest(id));
          }}
        />
      </div>
    </article>
  );
};
