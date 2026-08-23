import PropTypes from 'prop-types';
import { useCallback, useEffect } from 'react';

import { withRouter } from 'react-router-dom';

import ImmutablePropTypes from 'react-immutable-proptypes';
import { connect } from 'react-redux';

import { revealAccount } from '../actions/accounts';
import { openModal } from '../actions/modal';
import { fetchStatus } from '../actions/statuses';
import { Quote, QuoteError } from '../components/quote';
import { makeGetStatusWithExtraInfo, getAccountHidden } from '../selectors';
import { getQuoteState, getQuotedStatusId } from '../utils/status_quote';

const MAX_QUOTE_POSTS_NESTING_LEVEL = 1;

const makeMapStateToProps = () => {
  const getStatus = makeGetStatusWithExtraInfo();

  return (state, props) => {
    const quoteState = props.parentQuotePostId
      ? state.getIn(
        ['statuses', props.parentQuotePostId, 'quote', 'state'],
        getQuoteState(props.quote),
      )
      : getQuoteState(props.quote);
    const id = getQuotedStatusId(props.quote);
    const rawStatus = id ? state.getIn(['statuses', id]) : null;
    const isFetchFailure = Boolean(rawStatus?.get('quoteFetchFailed'));
    const statusResult = id && !isFetchFailure
      ? getStatus(state, { id, contextType: props.contextType })
      : { status: null, loadingState: 'not-found' };
    const { loadingState, status } = statusResult;
    const accountId = rawStatus?.get('account') ?? null;

    return {
      accountId,
      id,
      isFiltered: loadingState === 'filtered',
      isLimited: Boolean(accountId && getAccountHidden(state, accountId)),
      isFetchFailure,
      isLoading: loadingState === 'loading',
      quoteState,
      status,
    };
  };
};

const mapDispatchToProps = dispatch => ({
  dispatch,

  onOpenMedia (statusId, media, index, lang) {
    dispatch(openModal({
      modalType: 'MEDIA',
      modalProps: { statusId, media, index, lang },
    }));
  },

  onOpenVideo (statusId, media, lang, options) {
    dispatch(openModal({
      modalType: 'VIDEO',
      modalProps: { statusId, media, lang, options },
    }));
  },

  onRevealAccount (accountId) {
    dispatch(revealAccount(accountId));
  },
});

const QuotePresenter = props => {
  const {
    accountId,
    dispatch,
    id,
    isFiltered,
    isFetchFailure,
    isLimited,
    isLoading,
    onRevealAccount,
    parentQuotePostId,
    quoteState,
    status,
  } = props;

  useEffect(() => {
    if (quoteState === 'accepted' && id && !status && !isLoading && !isFetchFailure) {
      dispatch(fetchStatus(id, false, true, parentQuotePostId));
    }
  }, [
    dispatch,
    id,
    isFetchFailure,
    isLoading,
    parentQuotePostId,
    quoteState,
    status,
  ]);

  if (isFiltered) {
    return <QuoteError state='filtered' />;
  }

  if (quoteState !== 'accepted') {
    return <QuoteError state={quoteState} />;
  }

  if (!id) {
    return <QuoteError state='not_found' />;
  }

  if (!status) {
    return <QuoteError state='not_found' isLoading={!isFetchFailure} />;
  }

  if (isLimited) {
    return (
      <QuoteError
        accountId={accountId}
        onRevealAccount={onRevealAccount}
        state='limited'
      />
    );
  }

  return <Quote {...props} id={id} status={status} />;
};

QuotePresenter.propTypes = {
  accountId: PropTypes.string,
  id: PropTypes.string,
  dispatch: PropTypes.func.isRequired,
  isFiltered: PropTypes.bool,
  isFetchFailure: PropTypes.bool,
  isLimited: PropTypes.bool,
  isLoading: PropTypes.bool,
  onRevealAccount: PropTypes.func.isRequired,
  parentQuotePostId: PropTypes.string,
  quoteState: PropTypes.string.isRequired,
  status: ImmutablePropTypes.map,
};

const ConnectedQuote = connect(
  makeMapStateToProps,
  mapDispatchToProps,
)(withRouter(QuotePresenter));

const QuoteContainer = ({ contextType, nestingLevel = 1, parentQuotePostId, quote, variant = 'full' }) => {
  const renderNestedQuote = useCallback(
    (nestedQuote, nestedParentId, nextNestingLevel) => (
      <QuoteContainer
        contextType={contextType}
        nestingLevel={nextNestingLevel}
        parentQuotePostId={nestedParentId}
        quote={nestedQuote}
        variant={
          nextNestingLevel > MAX_QUOTE_POSTS_NESTING_LEVEL ? 'link' : 'full'
        }
      />
    ),
    [contextType],
  );

  return (
    <ConnectedQuote
      contextType={contextType}
      nestingLevel={nestingLevel}
      parentQuotePostId={parentQuotePostId}
      quote={quote}
      renderNestedQuote={renderNestedQuote}
      variant={variant}
    />
  );
};

QuoteContainer.propTypes = {
  contextType: PropTypes.string,
  nestingLevel: PropTypes.number,
  parentQuotePostId: PropTypes.string,
  quote: ImmutablePropTypes.map.isRequired,
  variant: PropTypes.oneOf(['full', 'link']),
};

export default QuoteContainer;
