import PropTypes from 'prop-types';
import { useCallback } from 'react';

import { defineMessages, useIntl } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';
import { useDispatch } from 'react-redux';

import { quoteCompose } from 'mastodon/actions/compose';
import { changeSetting } from 'mastodon/actions/settings';

import ConfirmationModal from './confirmation_modal';

const messages = defineMessages({
  title: {
    id: 'confirmations.quote_unlisted.title',
    defaultMessage: 'Quote a quiet public post?',
  },
  message: {
    id: 'confirmations.quote_unlisted.message',
    defaultMessage: 'This post is not listed in public timelines. Quoting it may make it more visible to others.',
  },
  confirm: {
    id: 'confirmations.quote_unlisted.confirm',
    defaultMessage: 'Quote post',
  },
  dontAskAgain: {
    id: 'confirmations.quote_unlisted.dont_ask_again',
    defaultMessage: "Quote and don't ask again",
  },
});

const QuietQuoteModal = ({ status, routerHistory, onClose }) => {
  const dispatch = useDispatch();
  const intl = useIntl();

  const handleConfirm = useCallback(() => {
    dispatch(quoteCompose(status, routerHistory, true));
  }, [dispatch, routerHistory, status]);

  const handleDontAskAgain = useCallback(() => {
    dispatch(changeSetting(['dismissed_banners', 'quote/quiet_post_hint'], true));
    dispatch(quoteCompose(status, routerHistory, true));
  }, [dispatch, routerHistory, status]);

  return (
    <ConfirmationModal
      title={intl.formatMessage(messages.title)}
      message={intl.formatMessage(messages.message)}
      confirm={intl.formatMessage(messages.confirm)}
      secondary={intl.formatMessage(messages.dontAskAgain)}
      onConfirm={handleConfirm}
      onSecondary={handleDontAskAgain}
      onClose={onClose}
    />
  );
};

QuietQuoteModal.propTypes = {
  status: ImmutablePropTypes.map.isRequired,
  routerHistory: PropTypes.object,
  onClose: PropTypes.func.isRequired,
};

export default QuietQuoteModal;
