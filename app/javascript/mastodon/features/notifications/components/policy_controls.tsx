/* eslint-disable react/jsx-no-bind, jsx-a11y/no-onchange */

import { useCallback } from 'react';

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';
import type { MessageDescriptor } from 'react-intl';

import { updateNotificationPolicy } from 'mastodon/actions/notification_policies';
import type {
  NotificationPolicyJSON,
  NotificationPolicyValue,
} from 'mastodon/api_types/notifications';
import { useAppDispatch, useAppSelector } from 'mastodon/store';

const messages = defineMessages({
  accept: { id: 'notifications.policy.accept', defaultMessage: 'Accept' },
  filter: { id: 'notifications.policy.filter', defaultMessage: 'Filter' },
  drop: { id: 'notifications.policy.drop', defaultMessage: 'Ignore' },
  notFollowingTitle: {
    id: 'notifications.policy.filter_not_following_title',
    defaultMessage: "People you don't follow",
  },
  notFollowingHint: {
    id: 'notifications.policy.filter_not_following_hint',
    defaultMessage: 'Until you manually approve them',
  },
  notFollowersTitle: {
    id: 'notifications.policy.filter_not_followers_title',
    defaultMessage: 'People not following you',
  },
  notFollowersHint: {
    id: 'notifications.policy.filter_not_followers_hint',
    defaultMessage: 'Including recent followers',
  },
  newAccountsTitle: {
    id: 'notifications.policy.filter_new_accounts_title',
    defaultMessage: 'New accounts',
  },
  newAccountsHint: {
    id: 'notifications.policy.filter_new_accounts.hint',
    defaultMessage: 'Created within the past 30 days',
  },
  privateMentionsTitle: {
    id: 'notifications.policy.filter_private_mentions_title',
    defaultMessage: 'Unsolicited private mentions',
  },
  privateMentionsHint: {
    id: 'notifications.policy.filter_private_mentions_hint',
    defaultMessage:
      'Unless you follow the sender or are already in the conversation',
  },
  limitedAccountsTitle: {
    id: 'notifications.policy.filter_limited_accounts_title',
    defaultMessage: 'Moderated accounts',
  },
  limitedAccountsHint: {
    id: 'notifications.policy.filter_limited_accounts_hint',
    defaultMessage: 'Limited by server moderators',
  },
});

const fields: {
  key: keyof Pick<
    NotificationPolicyJSON,
    | 'for_not_following'
    | 'for_not_followers'
    | 'for_new_accounts'
    | 'for_private_mentions'
    | 'for_limited_accounts'
  >;
  title: MessageDescriptor;
  hint: MessageDescriptor;
}[] = [
  {
    key: 'for_not_following',
    title: messages.notFollowingTitle,
    hint: messages.notFollowingHint,
  },
  {
    key: 'for_not_followers',
    title: messages.notFollowersTitle,
    hint: messages.notFollowersHint,
  },
  {
    key: 'for_new_accounts',
    title: messages.newAccountsTitle,
    hint: messages.newAccountsHint,
  },
  {
    key: 'for_private_mentions',
    title: messages.privateMentionsTitle,
    hint: messages.privateMentionsHint,
  },
  {
    key: 'for_limited_accounts',
    title: messages.limitedAccountsTitle,
    hint: messages.limitedAccountsHint,
  },
];

export const PolicyControls: React.FC = () => {
  const dispatch = useAppDispatch();
  const intl = useIntl();
  const policy = useAppSelector(
    (state) => state.notificationPolicy as NotificationPolicyJSON | null,
  );
  const onChange = useCallback(
    (key: (typeof fields)[number]['key'], value: NotificationPolicyValue) => {
      void dispatch(updateNotificationPolicy({ [key]: value }));
    },
    [dispatch],
  );

  if (!policy) return null;

  return (
    <section className='notification-43__policy'>
      <h3>
        <FormattedMessage
          id='notifications.policy.title'
          defaultMessage='Manage notifications from…'
        />
      </h3>
      {fields.map((field) => (
        <label key={field.key} className='notification-43__policy-row'>
          <span>
            <strong>
              <FormattedMessage {...field.title} />
            </strong>
            <small>
              <FormattedMessage
                {...field.hint}
                values={{
                  days: field.key === 'for_new_accounts' ? 30 : 3,
                }}
              />
            </small>
          </span>
          <select
            value={policy[field.key]}
            onChange={(event) => {
              onChange(
                field.key,
                event.target.value as NotificationPolicyValue,
              );
            }}
          >
            <option value='accept'>
              {intl.formatMessage(messages.accept)}
            </option>
            <option value='filter'>
              {intl.formatMessage(messages.filter)}
            </option>
            <option value='drop'>{intl.formatMessage(messages.drop)}</option>
          </select>
        </label>
      ))}
      <p className='notification-43__policy-summary'>
        <FormattedMessage
          id='notifications.policy.summary'
          defaultMessage='{requests, plural, one {# pending sender} other {# pending senders}} · {notifications, plural, one {# notification} other {# notifications}}'
          values={{
            requests: policy.summary.pending_requests_count,
            notifications: policy.summary.pending_notifications_count,
          }}
        />
      </p>
    </section>
  );
};
