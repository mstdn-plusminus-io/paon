/* eslint-disable react/jsx-no-bind */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';

import { Helmet } from 'react-helmet';

import {
  acceptNotificationRequests,
  dismissNotificationRequests,
  expandNotificationRequests,
  fetchNotificationRequests,
} from 'mastodon/actions/notification_requests';
import { changeSetting } from 'mastodon/actions/settings';
import Column from 'mastodon/components/column';
import ColumnHeader from 'mastodon/components/column_header';
import ScrollableList from 'mastodon/components/scrollable_list';
import type { NotificationRequestState } from 'mastodon/reducers/notification_requests';
import { useAppDispatch, useAppSelector } from 'mastodon/store';

import { NotificationRequestRow } from './components/notification_request_43';
import { PolicyControls } from './components/policy_controls';

const messages = defineMessages({
  title: {
    id: 'notification_requests.title',
    defaultMessage: 'Filtered notifications',
  },
  confirmAccept: {
    id: 'notification_requests.confirm_accept_multiple.message',
    defaultMessage: 'Accept the selected notification requests?',
  },
  confirmDismiss: {
    id: 'notification_requests.confirm_dismiss_multiple.message',
    defaultMessage: 'Dismiss the selected notification requests?',
  },
});

type ColumnHeaderProps = React.PropsWithChildren<Record<string, unknown>>;

const TypedColumnHeader =
  ColumnHeader as unknown as React.ComponentType<ColumnHeaderProps>;

export const NotificationRequests: React.FC<{ multiColumn?: boolean }> = ({
  multiColumn,
}) => {
  const dispatch = useAppDispatch();
  const intl = useIntl();
  const columnRef = useRef<Column>(null);
  const requestState = useAppSelector(
    (state) => state.notificationRequests as NotificationRequestState,
  );
  const requests = requestState.items;
  const isLoading = requestState.isLoading;
  const hasMore = Boolean(requestState.next);
  const minimized = useAppSelector(
    (state) =>
      (state.getIn(['settings', 'notifications', 'minimizeFilteredBanner']) as
        | boolean
        | undefined) ?? false,
  );
  const [selectionMode, setSelectionMode] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);

  useEffect(() => {
    void dispatch(fetchNotificationRequests());
  }, [dispatch]);

  const toggle = useCallback((id: string) => {
    setSelected((ids) =>
      ids.includes(id) ? ids.filter((item) => item !== id) : [...ids, id],
    );
  }, []);

  const allSelected =
    requests.length > 0 &&
    requests.every((request) => selected.includes(request.id));
  const toggleAll = useCallback(() => {
    setSelected(allSelected ? [] : requests.map((request) => request.id));
  }, [allSelected, requests]);

  const acceptSelected = useCallback(() => {
    if (
      selected.length > 0 &&
      window.confirm(
        intl.formatMessage(messages.confirmAccept, { count: selected.length }),
      )
    ) {
      void dispatch(acceptNotificationRequests(selected));
      setSelected([]);
    }
  }, [dispatch, intl, selected]);

  const dismissSelected = useCallback(() => {
    if (
      selected.length > 0 &&
      window.confirm(
        intl.formatMessage(messages.confirmDismiss, { count: selected.length }),
      )
    ) {
      void dispatch(dismissNotificationRequests(selected));
      setSelected([]);
    }
  }, [dispatch, intl, selected]);

  const headerContent = useMemo(
    () => (
      <div className='column-settings'>
        <div className='column-settings__row'>
          <label className='setting-toggle'>
            <input
              type='checkbox'
              checked={minimized}
              onChange={(event) => {
                dispatch(
                  changeSetting(
                    ['notifications', 'minimizeFilteredBanner'],
                    event.target.checked,
                  ),
                );
              }}
            />
            <span>
              <FormattedMessage
                id='notification_requests.minimize_banner'
                defaultMessage='Minimize filtered notifications banner'
              />
            </span>
          </label>
        </div>
        <PolicyControls />
      </div>
    ),
    [dispatch, minimized],
  );

  return (
    <Column
      bindToDocument={!multiColumn}
      ref={columnRef}
      label={intl.formatMessage(messages.title)}
    >
      <TypedColumnHeader
        icon='archive'
        title={intl.formatMessage(messages.title)}
        onClick={() => columnRef.current?.scrollTop()}
        multiColumn={multiColumn}
        showBackButton
      >
        {headerContent}
      </TypedColumnHeader>

      {requests.length > 0 && (
        <div className='notification-43__selection'>
          <label>
            <input type='checkbox' checked={allSelected} onChange={toggleAll} />
            <FormattedMessage
              id='notification_requests.select_all'
              defaultMessage='Select all'
            />
          </label>
          <button
            className='text-btn'
            onClick={() => {
              setSelectionMode((value) => !value);
            }}
          >
            {selectionMode ? (
              <FormattedMessage
                id='notification_requests.exit_selection'
                defaultMessage='Done'
              />
            ) : (
              <FormattedMessage
                id='notification_requests.edit_selection'
                defaultMessage='Edit'
              />
            )}
          </button>
          {selectionMode && (
            <>
              <button
                className='text-btn'
                disabled={selected.length === 0}
                onClick={acceptSelected}
              >
                <FormattedMessage
                  id='notification_requests.accept_multiple'
                  defaultMessage='Accept selected'
                  values={{ count: selected.length }}
                />
              </button>
              <button
                className='text-btn'
                disabled={selected.length === 0}
                onClick={dismissSelected}
              >
                <FormattedMessage
                  id='notification_requests.dismiss_multiple'
                  defaultMessage='Dismiss selected'
                  values={{ count: selected.length }}
                />
              </button>
            </>
          )}
        </div>
      )}

      <ScrollableList
        scrollKey='notification_requests'
        trackScroll={!multiColumn}
        bindToDocument={!multiColumn}
        isLoading={isLoading}
        showLoading={isLoading && requests.length === 0}
        hasMore={hasMore}
        onLoadMore={() => {
          dispatch(expandNotificationRequests());
        }}
        emptyMessage={
          <FormattedMessage
            id='empty_column.notification_requests'
            defaultMessage='All clear! There is nothing here.'
          />
        }
      >
        {requests.map((request) => (
          <NotificationRequestRow
            key={request.id}
            id={request.id}
            accountId={request.account_id}
            notificationsCount={request.notifications_count}
            selectionMode={selectionMode}
            checked={selected.includes(request.id)}
            onToggle={toggle}
          />
        ))}
      </ScrollableList>

      <Helmet>
        <title>{intl.formatMessage(messages.title)}</title>
        <meta name='robots' content='noindex' />
      </Helmet>
    </Column>
  );
};

export default NotificationRequests;
