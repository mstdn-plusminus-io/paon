/* eslint-disable jsx-a11y/no-noninteractive-tabindex */

import { FormattedMessage } from 'react-intl';

import classNames from 'classnames';

import type { ApiAccountRelationshipSeveranceEventJSON } from 'mastodon/api_types/notifications';
import { Icon } from 'mastodon/components/icon';
import { domain } from 'mastodon/initial_state';

export const NotificationSeveredRelationships: React.FC<{
  event: ApiAccountRelationshipSeveranceEventJSON;
  unread: boolean;
}> = ({ event, unread }) => {
  const target = (
    <strong>
      {event.target_name.trim().length > 0 ? event.target_name : '?'}
    </strong>
  );
  const values = {
    from: <strong>{domain}</strong>,
    target,
    name: target,
    followersCount: event.followers_count,
    followingCount: event.following_count,
  };
  let eventMessage: React.ReactNode;

  switch (event.type) {
    case 'account_suspension':
      eventMessage = (
        <FormattedMessage
          id='notification.relationships_severance_event.account_suspension'
          defaultMessage='An admin from {from} has suspended {target}, so you can no longer receive updates from them or interact with them.'
          values={values}
        />
      );
      break;
    case 'domain_block':
      eventMessage = (
        <FormattedMessage
          id='notification.relationships_severance_event.domain_block'
          defaultMessage='An admin from {from} has blocked {target}, including {followersCount} of your followers and {followingCount} accounts you follow.'
          values={values}
        />
      );
      break;
    case 'user_domain_block':
      eventMessage = (
        <FormattedMessage
          id='notification.relationships_severance_event.user_domain_block'
          defaultMessage='You have blocked {target}, removing {followersCount} of your followers and {followingCount} accounts you follow.'
          values={values}
        />
      );
      break;
    default:
      eventMessage = (
        <FormattedMessage
          id='notification.relationships_severance_event'
          defaultMessage='Lost connections with {name}'
          values={values}
        />
      );
  }

  return (
    <div
      className={classNames(
        'notification notification-severed-relationships focusable',
        { unread },
      )}
      tabIndex={0}
    >
      <div className='notification__message'>
        <div className='notification__favourite-icon-wrapper'>
          <Icon id='heart-broken' fixedWidth />
        </div>
        <span>{eventMessage}</span>
      </div>
      <div className='notification-43__details'>
        {event.purged && (
          <span className='notification-43__muted'>
            <FormattedMessage
              id='notification.relationships_severance_event.purged'
              defaultMessage='The remote data for this event has been purged.'
            />
          </span>
        )}
        <a
          href='/severed_relationships'
          target='_blank'
          rel='noopener noreferrer'
          className='button button-secondary'
        >
          <FormattedMessage
            id='notification.relationships_severance_event.learn_more'
            defaultMessage='Learn more'
          />
        </a>
      </div>
    </div>
  );
};
