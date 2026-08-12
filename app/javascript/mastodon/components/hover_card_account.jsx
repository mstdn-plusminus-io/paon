import PropTypes from 'prop-types';
import { forwardRef, useEffect } from 'react';

import { FormattedMessage } from 'react-intl';

import { List as ImmutableList } from 'immutable';
import { useDispatch, useSelector } from 'react-redux';

import { fetchAccount } from 'mastodon/actions/accounts';
import Account from 'mastodon/containers/account_container';

import { LoadingIndicator } from './loading_indicator';

export const HoverCardAccount = forwardRef(({ accountId }, ref) => {
  const dispatch = useDispatch();
  const account = useSelector(state => accountId ? state.getIn(['accounts', accountId]) : null);
  const note = useSelector(state => accountId ? state.getIn(['relationships', accountId, 'note'], '') : '');

  useEffect(() => {
    if (accountId && !account) {
      dispatch(fetchAccount(accountId));
    }
  }, [account, accountId, dispatch]);

  return (
    <div ref={ref} id='hover-card' role='tooltip' className={`hover-card dropdown-animation${account ? '' : ' hover-card--loading'}`}>
      {account ? (
        <>
          <Account id={accountId} />
          <div className='hover-card__content'>
            {account.get('note_emojified') ? <div className='hover-card__bio translate' dangerouslySetInnerHTML={{ __html: account.get('note_emojified') }} /> : null}
            {account.get('fields', ImmutableList()).take(2).map(field => (
              <dl className='hover-card__field' key={field.get('name')}>
                <dt dangerouslySetInnerHTML={{ __html: field.get('name_emojified') || field.get('name') }} />
                <dd dangerouslySetInnerHTML={{ __html: field.get('value_emojified') || field.get('value') }} />
              </dl>
            ))}
            {note ? (
              <dl className='hover-card__note'>
                <dt><FormattedMessage id='account.account_note_header' defaultMessage='Personal note' /></dt>
                <dd>{note}</dd>
              </dl>
            ) : null}
          </div>
        </>
      ) : <LoadingIndicator />}
    </div>
  );
});

HoverCardAccount.displayName = 'HoverCardAccount';

HoverCardAccount.propTypes = {
  accountId: PropTypes.string,
};
