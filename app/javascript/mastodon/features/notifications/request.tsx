/* eslint-disable react/jsx-no-bind */

import { useCallback, useEffect, useRef } from 'react';

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';

import { Helmet } from 'react-helmet';

import type { Account } from '@/types/resources';
import {
  acceptNotificationRequest,
  dismissNotificationRequest,
  expandNotificationsForRequest,
  fetchNotificationRequest,
  fetchNotificationsForRequest,
} from 'mastodon/actions/notification_requests';
import Column from 'mastodon/components/column';
import ColumnHeader from 'mastodon/components/column_header';
import { IconButton } from 'mastodon/components/icon_button';
import ScrollableList from 'mastodon/components/scrollable_list';
import type {
  NotificationItem,
  NotificationRequestState,
} from 'mastodon/reducers/notification_requests';
import { useAppDispatch, useAppSelector } from 'mastodon/store';

import NotificationContainer from './containers/notification_container';

const messages = defineMessages({
  title: {
    id: 'notification_requests.notifications_from',
    defaultMessage: 'Notifications from {name}',
  },
  accept: { id: 'notification_requests.accept', defaultMessage: 'Accept' },
  dismiss: { id: 'notification_requests.dismiss', defaultMessage: 'Dismiss' },
});

type ColumnHeaderProps = React.PropsWithChildren<Record<string, unknown>>;

interface AccountWithLimited {
  get: (key: 'limited') => boolean | undefined;
}

const TypedColumnHeader =
  ColumnHeader as unknown as React.ComponentType<ColumnHeaderProps>;

const notificationString = (item: NotificationItem, key: string) => {
  const value = item.get(key);
  return typeof value === 'string' ? value : undefined;
};

export const NotificationRequest: React.FC<{
  multiColumn?: boolean;
  params: { id: string };
}> = ({ multiColumn, params: { id } }) => {
  const dispatch = useAppDispatch();
  const intl = useIntl();
  const columnRef = useRef<Column>(null);
  const current = useAppSelector(
    (state) => (state.notificationRequests as NotificationRequestState).current,
  );
  const request = current.item?.id === id ? current.item : null;
  const accountId = request?.account_id;
  const account = useAppSelector((state) =>
    accountId
      ? (state.getIn(['accounts', accountId]) as Account | undefined)
      : undefined,
  );
  const notifications = current.notifications.items;

  useEffect(() => {
    void dispatch(fetchNotificationRequest(id));
  }, [dispatch, id]);

  useEffect(() => {
    if (accountId) void dispatch(fetchNotificationsForRequest(accountId));
  }, [accountId, dispatch]);

  const selectChild = useCallback(
    (notificationId: string, offset: number) => {
      const index =
        notifications.findIndex(
          (item) => notificationString(item, 'id') === notificationId,
        ) + offset;
      const container = columnRef.current?.node as HTMLElement | undefined;
      const element = container?.querySelector<HTMLElement>(
        `article:nth-of-type(${index + 1}) .focusable`,
      );
      element?.focus();
    },
    [notifications],
  );

  const displayName = account?.get('display_name') ?? '';
  const username = account?.get('username') ?? '';
  const title = intl.formatMessage(messages.title, {
    name: displayName.trim() || username.trim() || '…',
  });
  const limited = account
    ? Boolean((account as unknown as AccountWithLimited).get('limited'))
    : false;

  return (
    <Column bindToDocument={!multiColumn} ref={columnRef} label={title}>
      <TypedColumnHeader
        icon='archive'
        title={title}
        onClick={() => {
          columnRef.current?.scrollTop();
        }}
        multiColumn={multiColumn}
        showBackButton
        extraButton={
          !current.removed && request ? (
            <>
              <IconButton
                icon='times'
                title={intl.formatMessage(messages.dismiss)}
                onClick={() => {
                  void dispatch(dismissNotificationRequest(id));
                }}
              />
              <IconButton
                icon='check'
                title={intl.formatMessage(messages.accept)}
                onClick={() => {
                  void dispatch(acceptNotificationRequest(id));
                }}
              />
            </>
          ) : undefined
        }
      />

      {limited && account && (
        <div className='dismissable-banner'>
          {account.get('acct').includes('@') ? (
            <FormattedMessage
              id='notification_requests.explainer_for_limited_remote_account'
              defaultMessage='Notifications from this account have been filtered because the account or its server has been limited by a moderator.'
            />
          ) : (
            <FormattedMessage
              id='notification_requests.explainer_for_limited_account'
              defaultMessage='Notifications from this account have been filtered because the account has been limited by a moderator.'
            />
          )}
        </div>
      )}
      {current.removed && (
        <div className='dismissable-banner'>
          <FormattedMessage
            id='notification_requests.request_handled'
            defaultMessage='This notification request has been handled.'
          />
        </div>
      )}

      <ScrollableList
        scrollKey={`notification_requests/${id}`}
        trackScroll={!multiColumn}
        bindToDocument={!multiColumn}
        isLoading={current.isLoading || current.notifications.isLoading}
        showLoading={
          (current.isLoading || current.notifications.isLoading) &&
          notifications.length === 0
        }
        hasMore={Boolean(current.notifications.next)}
        onLoadMore={() => {
          if (accountId) dispatch(expandNotificationsForRequest(accountId));
        }}
        emptyMessage={
          <FormattedMessage
            id='notification_requests.request_handled'
            defaultMessage='This notification request has been handled.'
          />
        }
      >
        {notifications.map((item) => {
          const notificationId = notificationString(item, 'id');
          if (!notificationId) return null;

          return (
            <NotificationContainer
              key={notificationId}
              notification={item}
              accountId={notificationString(item, 'account')}
              onMoveUp={(childId: string) => {
                selectChild(childId, -1);
              }}
              onMoveDown={(childId: string) => {
                selectChild(childId, 1);
              }}
            />
          );
        })}
      </ScrollableList>

      <Helmet>
        <title>{title}</title>
        <meta name='robots' content='noindex' />
      </Helmet>
    </Column>
  );
};

export default NotificationRequest;
