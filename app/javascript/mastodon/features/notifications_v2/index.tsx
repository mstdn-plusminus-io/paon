/* eslint-disable react/jsx-no-bind */

import { useCallback, useEffect, useRef } from 'react';

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';

import { Helmet } from 'react-helmet';
import { Link } from 'react-router-dom';

import type { Map as ImmutableMap } from 'immutable';

import { useDebouncedCallback } from 'use-debounce';

import { addColumn, moveColumn, removeColumn } from 'mastodon/actions/columns';
import { submitMarkers } from 'mastodon/actions/markers';
import {
  fetchNotificationGroups,
  fetchNotificationGroupsGap,
  loadPendingNotificationGroups,
  markNotificationGroupsAsRead,
  mountNotificationGroups,
  pollRecentNotificationGroups,
  unmountNotificationGroups,
  updateNotificationGroupsScroll,
} from 'mastodon/actions/notification_groups';
import { compareId } from 'mastodon/compare_id';
import Column from 'mastodon/components/column';
import ColumnHeader from 'mastodon/components/column_header';
import { Icon } from 'mastodon/components/icon';
import { NotSignedInIndicator } from 'mastodon/components/not_signed_in_indicator';
import ScrollableList from 'mastodon/components/scrollable_list';
import { FilteredNotificationsBanner } from 'mastodon/features/notifications/components/filtered_notifications_banner';
import NotificationsPermissionBanner from 'mastodon/features/notifications/components/notifications_permission_banner';
import ColumnSettingsContainer from 'mastodon/features/notifications/containers/column_settings_container';
import { me } from 'mastodon/initial_state';
import type {
  NotificationGap,
  NotificationGroupsState,
} from 'mastodon/reducers/notification_groups';
import {
  selectAnyUnreadNotificationGroup,
  selectNotificationGroups,
  selectPendingNotificationGroupsCount,
  selectUnreadNotificationGroupsCount,
} from 'mastodon/selectors/notification_groups';
import { useAppDispatch, useAppSelector } from 'mastodon/store';

import { NotificationGroup } from './components/notification_group';
import { FilterBar } from './filter_bar';

const messages = defineMessages({
  title: { id: 'column.notifications', defaultMessage: 'Notifications' },
  markAsRead: {
    id: 'notifications.mark_as_read',
    defaultMessage: 'Mark every notification as read',
  },
  filtered: {
    id: 'notification_requests.title',
    defaultMessage: 'Filtered notifications',
  },
});

type ColumnHeaderProps = React.PropsWithChildren<Record<string, unknown>>;

const TypedColumnHeader =
  ColumnHeader as unknown as React.ComponentType<ColumnHeaderProps>;

export const Notifications: React.FC<{
  columnId?: string;
  multiColumn?: boolean;
}> = ({ columnId, multiColumn }) => {
  const dispatch = useAppDispatch();
  const intl = useIntl();
  const columnRef = useRef<Column>(null);
  const notificationGroups = useAppSelector(
    (state) => state.notificationGroups as NotificationGroupsState,
  );
  const groups = useAppSelector(selectNotificationGroups);
  const isLoading = notificationGroups.isLoading;
  const showUnread = useAppSelector(
    (state) =>
      (state.getIn(['settings', 'notifications', 'showUnread']) as
        | boolean
        | undefined) ?? true,
  );
  const lastReadId = showUnread ? notificationGroups.readMarkerId : '0';
  const numPending = useAppSelector(selectPendingNotificationGroupsCount);
  const unreadCount = useAppSelector(selectUnreadNotificationGroupsCount);
  const canMarkRead = useAppSelector(selectAnyUnreadNotificationGroup);
  const needsReload = notificationGroups.mergedNotifications === 'needs-reload';
  const showFilterBar = useAppSelector(
    (state) =>
      (state.getIn(['settings', 'notifications', 'quickFilter', 'show']) as
        | boolean
        | undefined) ?? true,
  );
  const needsPermission = useAppSelector((state) => {
    const alerts = state.getIn(['settings', 'notifications', 'alerts']) as
      | ImmutableMap<string, boolean>
      | undefined;

    return Boolean(
      alerts?.includes(true) &&
        state.getIn(['notifications', 'browserSupport']) &&
        state.getIn(['notifications', 'browserPermission']) === 'default' &&
        !state.getIn(['settings', 'notifications', 'dismissPermissionBanner']),
    );
  });
  const hasMore = groups.at(-1)?.kind === 'gap';
  const signedIn = Boolean(me);
  const pinned = Boolean(columnId);

  useEffect(() => {
    void dispatch(fetchNotificationGroups());
    dispatch(mountNotificationGroups());
    const interval = window.setInterval(() => {
      if (document.visibilityState === 'visible')
        void dispatch(pollRecentNotificationGroups());
    }, 30_000);
    return () => {
      window.clearInterval(interval);
      dispatch(unmountNotificationGroups());
      dispatch(updateNotificationGroupsScroll(false));
    };
  }, [dispatch]);

  const loadGap = useCallback(
    (gap: NotificationGap) => {
      void dispatch(fetchNotificationGroupsGap(gap));
    },
    [dispatch],
  );
  const loadOlder = useDebouncedCallback(
    () => {
      const gap = groups.at(-1);
      if (gap?.kind === 'gap') void dispatch(fetchNotificationGroupsGap(gap));
    },
    300,
    { leading: true },
  );
  const scrollTop = useDebouncedCallback(() => {
    dispatch(updateNotificationGroupsScroll(true));
  }, 100);
  const scroll = useDebouncedCallback(() => {
    dispatch(updateNotificationGroupsScroll(false));
  }, 100);

  useEffect(
    () => () => {
      loadOlder.cancel();
      scrollTop.cancel();
      scroll.cancel();
    },
    [loadOlder, scroll, scrollTop],
  );

  const selectChild = useCallback(
    (id: string, offset: number) => {
      const index =
        groups.findIndex(
          (item) => item.kind === 'notification' && item.group_key === id,
        ) + offset;
      const container = columnRef.current?.node as HTMLElement | undefined;
      const element = container?.querySelector<HTMLElement>(
        `article:nth-of-type(${index + 1}) .focusable`,
      );
      element?.focus();
    },
    [groups],
  );

  const content = groups.map((item) =>
    item.kind === 'gap' ? (
      <button
        key={`gap:${item.url ?? item.maxId ?? item.sinceId ?? 'unknown'}`}
        className='load-more load-gap'
        disabled={isLoading}
        onClick={() => {
          loadGap(item);
        }}
        aria-label={intl.formatMessage({
          id: 'status.load_more',
          defaultMessage: 'Load more',
        })}
      >
        <Icon id='ellipsis-h' />
      </button>
    ) : (
      <NotificationGroup
        key={item.group_key}
        group={item}
        unread={
          lastReadId !== '0' &&
          Boolean(item.page_max_id) &&
          typeof item.page_max_id === 'string' &&
          compareId(item.page_max_id, lastReadId) > 0
        }
        onMoveUp={(id) => {
          selectChild(id, -1);
        }}
        onMoveDown={(id) => {
          selectChild(id, 1);
        }}
      />
    ),
  );

  const extraButton = (
    <>
      <Link
        to='/notifications/requests'
        className='column-header__button'
        title={intl.formatMessage(messages.filtered)}
        aria-label={intl.formatMessage(messages.filtered)}
      >
        <Icon id='archive' fixedWidth />
      </Link>
      {canMarkRead && (
        <button
          className='column-header__button'
          title={intl.formatMessage(messages.markAsRead)}
          aria-label={intl.formatMessage(messages.markAsRead)}
          onClick={() => {
            dispatch(markNotificationGroupsAsRead());
            dispatch(submitMarkers({ immediate: true }));
          }}
        >
          <Icon id='check-double' fixedWidth />
        </button>
      )}
    </>
  );

  return (
    <Column
      bindToDocument={!multiColumn}
      ref={columnRef}
      label={intl.formatMessage(messages.title)}
    >
      <TypedColumnHeader
        icon='bell'
        active={unreadCount > 0 || needsReload}
        title={intl.formatMessage(messages.title)}
        onPin={() => {
          if (columnId) dispatch(removeColumn(columnId));
          else dispatch(addColumn('NOTIFICATIONS', {}));
        }}
        onMove={(direction: number) => {
          if (columnId) dispatch(moveColumn(columnId, direction));
        }}
        onClick={() => {
          columnRef.current?.scrollTop();
        }}
        pinned={pinned}
        multiColumn={multiColumn}
        extraButton={extraButton}
      >
        <ColumnSettingsContainer />
      </TypedColumnHeader>

      {signedIn && showFilterBar && <FilterBar />}
      {signedIn ? (
        <ScrollableList
          scrollKey={`notifications-${columnId}`}
          trackScroll={!pinned}
          bindToDocument={!multiColumn}
          isLoading={isLoading}
          showLoading={isLoading && groups.length === 0}
          hasMore={hasMore}
          numPending={numPending}
          prepend={
            <>
              {needsPermission && <NotificationsPermissionBanner />}
              <FilteredNotificationsBanner />
            </>
          }
          alwaysPrepend
          emptyMessage={
            <FormattedMessage
              id='empty_column.notifications'
              defaultMessage="You don't have any notifications yet. When other people interact with you, you will see it here."
            />
          }
          onLoadMore={loadOlder}
          onLoadPending={() => {
            dispatch(loadPendingNotificationGroups());
          }}
          onScrollToTop={scrollTop}
          onScroll={scroll}
        >
          {content}
        </ScrollableList>
      ) : (
        <NotSignedInIndicator />
      )}

      <Helmet>
        <title>{intl.formatMessage(messages.title)}</title>
        <meta name='robots' content='noindex' />
      </Helmet>
    </Column>
  );
};

export default Notifications;
