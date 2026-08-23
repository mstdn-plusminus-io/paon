import PropTypes from 'prop-types';
import { useCallback, useEffect, useState } from 'react';

import { defineMessages, injectIntl } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';
import { connect } from 'react-redux';

import {
  clearPendingReplies,
  completeContextRefresh,
  fetchContext,
  showPendingReplies,
} from 'mastodon/actions/statuses';
import api from 'mastodon/api';

const messages = defineMessages({
  loading: { id: 'status.context.loading', defaultMessage: 'Looking for more replies…' },
  more: { id: 'status.context.more_replies_found', defaultMessage: 'More replies found' },
  show: { id: 'status.context.show', defaultMessage: 'Show' },
  dismiss: { id: 'status.context.dismiss', defaultMessage: 'Dismiss' },
  error: { id: 'status.context.loading_error', defaultMessage: "Couldn't load new replies" },
  retry: { id: 'status.context.retry', defaultMessage: 'Retry' },
  success: { id: 'status.context.loading_success', defaultMessage: 'New replies loaded' },
});

const mapStateToProps = (state, { statusId }) => ({
  refresh: state.getIn(['contexts', 'refreshing', statusId]),
  pendingCount: state.getIn(['contexts', 'pendingReplies', statusId], []).size || 0,
});

const RefreshController = ({
  statusId,
  statusCreatedAt,
  isLocal,
  refresh,
  pendingCount,
  intl,
  dispatch,
}) => {
  const [state, setState] = useState(refresh ? 'loading' : 'idle');
  const [visible, setVisible] = useState(() => document.visibilityState === 'visible');
  const [dismissed, setDismissed] = useState(false);

  const checkContext = useCallback(() => {
    setDismissed(false);
    setState('loading');
    return dispatch(fetchContext(statusId, true))
      .then(succeeded => setState(succeeded ? 'idle' : 'error'));
  }, [dispatch, statusId]);

  useEffect(() => {
    const onVisibility = () => setVisible(document.visibilityState === 'visible');
    document.addEventListener('visibilitychange', onVisibility);
    return () => document.removeEventListener('visibilitychange', onVisibility);
  }, []);

  useEffect(() => {
    if (visible && !dismissed) {
      void dispatch(fetchContext(statusId, true));
    }
  }, [dispatch, dismissed, statusId, visible]);

  useEffect(() => {
    const age = Date.now() - new Date(statusCreatedAt).getTime();
    const interval = age < 30 * 60_000 ? 60_000 : 5 * 60_000;
    const tick = () => {
      if (visible && state !== 'loading' && !dismissed) {
        void dispatch(fetchContext(statusId, true));
      }
    };
    const intervalId = window.setInterval(tick, interval);

    return () => window.clearInterval(intervalId);
  }, [dispatch, dismissed, state, statusCreatedAt, statusId, visible]);

  useEffect(() => {
    if (isLocal || !refresh || !visible || dismissed) return undefined;

    let cancelled = false;
    let timeoutId;
    const refreshId = refresh.get('id');
    const retry = Math.max(1, refresh.get('retry', 5));

    const poll = (iteration = 1) => {
      timeoutId = window.setTimeout(() => {
        api().get(`/api/v1_alpha/async_refreshes/${refreshId}`).then(response => {
          if (cancelled) return undefined;
          const result = response.data.async_refresh;
          const finished = result.status === 'finished';
          const longRunning = iteration === 3;

          if (!finished && !longRunning) {
            poll(iteration + 1);
            return undefined;
          }

          if (finished) {
            dispatch(completeContextRefresh(statusId));
          }

          if (result.result_count > 0) {
            void dispatch(fetchContext(statusId, true)).then(succeeded => {
              if (!succeeded) {
                setState('error');
              } else if (finished) {
                setState('idle');
              } else {
                poll(iteration + 1);
              }
            });
            return undefined;
          }

          if (finished) setState('idle');
          else poll(iteration + 1);
          return undefined;
        }).catch(() => {
          if (!cancelled) setState('error');
        });
      }, retry * 1000);
    };

    setState('loading');
    poll();

    return () => {
      cancelled = true;
      window.clearTimeout(timeoutId);
    };
  }, [dismissed, dispatch, isLocal, refresh, statusId, visible]);

  useEffect(() => () => {
    dispatch(clearPendingReplies(statusId));
  }, [dispatch, statusId]);

  const handleShowReplies = useCallback(() => {
    dispatch(showPendingReplies(statusId));
    setState('success');
  }, [dispatch, statusId]);

  const handleDismiss = useCallback(() => {
    dispatch(clearPendingReplies(statusId));
    setDismissed(true);
    setState('idle');
  }, [dispatch, statusId]);

  useEffect(() => {
    if (state !== 'success') return undefined;
    const timeoutId = window.setTimeout(() => setState('idle'), 2500);
    return () => window.clearTimeout(timeoutId);
  }, [state]);

  if (pendingCount > 0) {
    return (
      <div className='thread-refresh' role='status' aria-live='polite'>
        <span>{intl.formatMessage(messages.more)}</span>
        <button type='button' className='button button-secondary' onClick={handleShowReplies}>
          {intl.formatMessage(messages.show)}
        </button>
        <button type='button' className='text-btn' onClick={handleDismiss}>
          {intl.formatMessage(messages.dismiss)}
        </button>
      </div>
    );
  }

  if (state === 'loading') {
    return <div className='thread-refresh' role='status' aria-live='polite' aria-busy='true'>{intl.formatMessage(messages.loading)}</div>;
  }

  if (state === 'error') {
    return (
      <div className='thread-refresh' role='status' aria-live='polite'>
        <span>{intl.formatMessage(messages.error)}</span>
        <button type='button' className='button button-secondary' onClick={checkContext}>{intl.formatMessage(messages.retry)}</button>
      </div>
    );
  }

  if (state === 'success') {
    return <div className='thread-refresh' role='status' aria-live='polite'>{intl.formatMessage(messages.success)}</div>;
  }

  return null;
};

RefreshController.propTypes = {
  statusId: PropTypes.string.isRequired,
  statusCreatedAt: PropTypes.string.isRequired,
  isLocal: PropTypes.bool.isRequired,
  refresh: ImmutablePropTypes.map,
  pendingCount: PropTypes.number.isRequired,
  intl: PropTypes.object.isRequired,
  dispatch: PropTypes.func.isRequired,
};

export default connect(mapStateToProps)(injectIntl(RefreshController));
