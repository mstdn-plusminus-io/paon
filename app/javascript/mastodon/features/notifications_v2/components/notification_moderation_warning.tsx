/* eslint-disable jsx-a11y/no-noninteractive-tabindex */

import { FormattedMessage } from 'react-intl';

import classNames from 'classnames';

import type { ApiAccountWarningJSON } from 'mastodon/api_types/notifications';
import { Icon } from 'mastodon/components/icon';

export const NotificationModerationWarning: React.FC<{
  warning: ApiAccountWarningJSON;
  unread: boolean;
}> = ({ warning, unread }) => {
  const firstStatusId = warning.status_ids[0];
  const targetAcct = warning.target_account?.acct;
  let actionMessage: React.ReactNode;

  switch (warning.action) {
    case 'disable':
      actionMessage = (
        <FormattedMessage
          id='notification.moderation_warning.action_disable'
          defaultMessage='Your account has been disabled.'
        />
      );
      break;
    case 'mark_statuses_as_sensitive':
      actionMessage = (
        <FormattedMessage
          id='notification.moderation_warning.action_mark_statuses_as_sensitive'
          defaultMessage='Some of your posts have been marked as sensitive.'
        />
      );
      break;
    case 'delete_statuses':
      actionMessage = (
        <FormattedMessage
          id='notification.moderation_warning.action_delete_statuses'
          defaultMessage='Some of your posts have been removed.'
        />
      );
      break;
    case 'sensitive':
      actionMessage = (
        <FormattedMessage
          id='notification.moderation_warning.action_sensitive'
          defaultMessage='Your posts will be marked as sensitive from now on.'
        />
      );
      break;
    case 'silence':
      actionMessage = (
        <FormattedMessage
          id='notification.moderation_warning.action_silence'
          defaultMessage='Your account has been limited.'
        />
      );
      break;
    case 'suspend':
      actionMessage = (
        <FormattedMessage
          id='notification.moderation_warning.action_suspend'
          defaultMessage='Your account has been suspended.'
        />
      );
      break;
    case 'none':
    default:
      actionMessage = (
        <FormattedMessage
          id='notification.moderation_warning.action_none'
          defaultMessage='Your account has received a moderation warning.'
        />
      );
  }

  return (
    <div
      className={classNames(
        'notification notification-moderation-warning focusable',
        { unread },
      )}
      tabIndex={0}
    >
      <div className='notification__message'>
        <div className='notification__favourite-icon-wrapper'>
          <Icon id='gavel' fixedWidth />
        </div>
        <span>{actionMessage}</span>
      </div>
      <div className='notification-43__details'>
        {warning.text && <p>{warning.text}</p>}
        <div className='notification-43__links'>
          {firstStatusId && targetAcct && (
            <a
              href={`/@${targetAcct}/${firstStatusId}`}
              className='link-button'
            >
              <FormattedMessage
                id='notification.moderation_warning.view_status'
                defaultMessage='View affected post'
              />
            </a>
          )}
          <a
            href={`/disputes/strikes/${warning.id}`}
            target='_blank'
            rel='noopener noreferrer'
            className='button button-secondary'
          >
            <FormattedMessage
              id='notification.moderation-warning.learn_more'
              defaultMessage='Learn more'
            />
          </a>
        </div>
      </div>
    </div>
  );
};
