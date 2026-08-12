/* eslint-disable jsx-a11y/no-noninteractive-tabindex, react/jsx-no-bind */

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';

import classNames from 'classnames';
import { Link } from 'react-router-dom';

import { fromJS, Map as ImmutableMap } from 'immutable';

import type { Account } from '@/types/resources';
import { dismissNotificationGroup } from 'mastodon/actions/notification_groups';
import { Avatar } from 'mastodon/components/avatar';
import { Icon } from 'mastodon/components/icon';
import { IconButton } from 'mastodon/components/icon_button';
import AccountContainer from 'mastodon/containers/account_container';
import StatusContainer from 'mastodon/containers/status_container';
import NotificationContainer from 'mastodon/features/notifications/containers/notification_container';
import type { NotificationGroup as NotificationGroupModel } from 'mastodon/models/notification_group';
import { useAppDispatch, useAppSelector } from 'mastodon/store';

import { NotificationModerationWarning } from './notification_moderation_warning';
import { NotificationSeveredRelationships } from './notification_severed_relationships';

const messages = defineMessages({
  dismiss: {
    id: 'notifications.dismiss',
    defaultMessage: 'Dismiss notification',
  },
});

const iconForType = (type: string) => {
  switch (type) {
    case 'favourite':
      return 'star';
    case 'reblog':
      return 'retweet';
    case 'follow':
      return 'user-plus';
    default:
      return 'bell';
  }
};

const GroupAvatars: React.FC<{ ids: string[] }> = ({ ids }) => {
  const accounts = useAppSelector((state) =>
    ids.flatMap((id) => {
      const account = state.getIn(['accounts', id]) as Account | undefined;
      return account ? [account] : [];
    }),
  );

  return (
    <span className='notification-43__avatars' aria-hidden='true'>
      {accounts.slice(0, 4).map((account) => (
        <Avatar key={account.get('id')} account={account} size={28} />
      ))}
    </span>
  );
};

const GroupSummary: React.FC<{
  group: NotificationGroupModel;
  account: Account | undefined;
  unread: boolean;
}> = ({ group, account, unread }) => {
  const count = Math.max(0, group.notifications_count - 1);
  const displayName = account?.get('display_name_html') ?? '';
  const name = account ? (
    <bdi>
      <Link
        className='notification__display-name'
        to={`/@${account.get('acct')}`}
        data-hover-card-account={account.get('id')}
      >
        <strong
          dangerouslySetInnerHTML={{
            __html:
              displayName.trim().length > 0
                ? displayName
                : account.get('username'),
          }}
        />
      </Link>
    </bdi>
  ) : (
    <FormattedMessage
      id='notification.unknown_account'
      defaultMessage='An unavailable account'
    />
  );
  const values = {
    name,
    count,
    a: (chunks: React.ReactNode) => <span>{chunks}</span>,
  };
  const message =
    group.type === 'follow' ? (
      <FormattedMessage
        id='notification.follow.name_and_others'
        defaultMessage='{name} and {count} others followed you'
        values={values}
      />
    ) : group.type === 'favourite' ? (
      <FormattedMessage
        id='notification.favourite.name_and_others_with_link'
        defaultMessage='{name} and {count} others favorited your post'
        values={values}
      />
    ) : (
      <FormattedMessage
        id='notification.reblog.name_and_others_with_link'
        defaultMessage='{name} and {count} others boosted your post'
        values={values}
      />
    );

  return (
    <div
      className={classNames('notification notification-43__group focusable', {
        unread,
      })}
      tabIndex={0}
    >
      <div className='notification__message'>
        <div className='notification__favourite-icon-wrapper'>
          <Icon id={iconForType(group.type)} fixedWidth />
        </div>
        <span>{message}</span>
      </div>
      <GroupAvatars ids={group.sampleAccountIds} />
      {group.type === 'follow' && group.sampleAccountIds[0] && (
        <AccountContainer id={group.sampleAccountIds[0]} />
      )}
      {(group.type === 'favourite' || group.type === 'reblog') &&
        group.statusId && (
          <StatusContainer
            id={group.statusId}
            account={account}
            muted
            withDismiss
            contextType='notifications'
          />
        )}
    </div>
  );
};

export const NotificationGroup: React.FC<{
  group: NotificationGroupModel;
  unread: boolean;
  onMoveUp: (id: string) => void;
  onMoveDown: (id: string) => void;
}> = ({ group, unread, onMoveUp, onMoveDown }) => {
  const dispatch = useAppDispatch();
  const intl = useIntl();
  const firstAccountId = group.sampleAccountIds[0];
  const account = useAppSelector((state) =>
    firstAccountId
      ? (state.getIn(['accounts', firstAccountId]) as Account | undefined)
      : undefined,
  );
  const dismiss = () => {
    void dispatch(dismissNotificationGroup(group.group_key));
  };
  let content: React.ReactNode;

  if (group.type === 'severed_relationships' && group.event) {
    content = (
      <NotificationSeveredRelationships event={group.event} unread={unread} />
    );
  } else if (group.type === 'moderation_warning' && group.moderationWarning) {
    content = (
      <NotificationModerationWarning
        warning={group.moderationWarning}
        unread={unread}
      />
    );
  } else if (
    group.notifications_count > 1 &&
    ['follow', 'favourite', 'reblog'].includes(group.type)
  ) {
    content = <GroupSummary group={group} account={account} unread={unread} />;
  } else if (account && firstAccountId) {
    const notification = ImmutableMap<string, unknown>({
      id: group.group_key,
      type: group.type,
      account: firstAccountId,
      created_at:
        group.latest_page_notification_at ?? new Date(0).toISOString(),
      status: group.statusId ?? null,
      report: group.report ? fromJS(group.report) : null,
      event: group.event ? fromJS(group.event) : null,
      moderation_warning: group.moderationWarning
        ? fromJS(group.moderationWarning)
        : null,
    });
    content = (
      <NotificationContainer
        notification={notification}
        accountId={firstAccountId}
        onMoveUp={onMoveUp}
        onMoveDown={onMoveDown}
        unread={unread}
      />
    );
  } else {
    content = (
      <div
        className={classNames(
          'notification notification-43__unknown focusable',
          { unread },
        )}
        tabIndex={0}
      >
        <div className='notification__message'>
          <div className='notification__favourite-icon-wrapper'>
            <Icon id='bell' fixedWidth />
          </div>
          <span>
            <FormattedMessage
              id='notification.unknown'
              defaultMessage='A notification is no longer available.'
            />
          </span>
        </div>
      </div>
    );
  }

  return (
    <article
      className='notification-43__wrapper'
      data-group-key={group.group_key}
    >
      {content}
      <IconButton
        className='notification-43__dismiss'
        icon='times'
        title={intl.formatMessage(messages.dismiss)}
        onClick={dismiss}
      />
    </article>
  );
};
