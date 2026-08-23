import PropTypes from 'prop-types';

import { defineMessages, injectIntl } from 'react-intl';

import { fromJS } from 'immutable';
import { connect } from 'react-redux';

import CloseIcon from '@/material-icons/400-24px/close.svg?react';
import { cancelQuoteCompose } from 'mastodon/actions/compose';
import { IconButton } from 'mastodon/components/icon_button';
import QuoteContainer from 'mastodon/containers/quote_container';

const messages = defineMessages({
  remove: {
    id: 'compose_form.quote.remove',
    defaultMessage: 'Remove quoted post',
  },
  loading: {
    id: 'compose_form.quote.loading',
    defaultMessage: 'Finding quoted post…',
  },
});

const mapStateToProps = state => ({
  quotedStatusId: state.getIn(['compose', 'quoted_status_id']),
  fetchingLink: Boolean(state.getIn(['compose', 'fetching_link'])),
  isEditing: Boolean(state.getIn(['compose', 'id'])),
});

const QuotedPost = ({ quotedStatusId, fetchingLink, isEditing, intl, onCancel }) => {
  if (!quotedStatusId && !fetchingLink) return null;

  if (!quotedStatusId) {
    return (
      <div className='compose-form__quoted-post compose-form__quoted-post--loading' role='status'>
        {intl.formatMessage(messages.loading)}
      </div>
    );
  }

  const quote = fromJS({ state: 'accepted', quoted_status: quotedStatusId });

  return (
    <div className='compose-form__quoted-post'>
      {!isEditing && (
        <IconButton
          className='compose-form__quoted-post__remove'
          icon='times'
          iconComponent={CloseIcon}
          title={intl.formatMessage(messages.remove)}
          onClick={onCancel}
        />
      )}
      <QuoteContainer contextType='composer' quote={quote} />
    </div>
  );
};

QuotedPost.propTypes = {
  quotedStatusId: PropTypes.string,
  fetchingLink: PropTypes.bool,
  isEditing: PropTypes.bool,
  intl: PropTypes.object.isRequired,
  onCancel: PropTypes.func.isRequired,
};

export default connect(mapStateToProps, { onCancel: cancelQuoteCompose })(injectIntl(QuotedPost));
