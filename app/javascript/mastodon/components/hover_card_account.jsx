import PropTypes from 'prop-types';
import { forwardRef, useEffect } from 'react';

import { FormattedMessage } from 'react-intl';

import { Link } from 'react-router-dom';

import { List as ImmutableList } from 'immutable';
import { useDispatch, useSelector } from 'react-redux';

import { fetchAccount } from 'mastodon/actions/accounts';
import { fetchAccountsFamiliarFollowers } from 'mastodon/actions/accounts_familiar_followers';
import Account from 'mastodon/containers/account_container';
import { FamiliarFollowersReadout } from 'mastodon/features/account/components/familiar_followers';
import { domain } from 'mastodon/initial_state';
import { getAccountFamiliarFollowers, getAccountHidden } from 'mastodon/selectors';

import { Avatar } from './avatar';
import { DisplayName } from './display_name';
import { LoadingIndicator } from './loading_indicator';

export const HoverCardAccount = forwardRef(({ accountId }, ref) => {
  const dispatch = useDispatch();
  const account = useSelector(state => accountId ? state.getIn(['accounts', accountId]) : null);
  const hidden = useSelector(state => accountId ? getAccountHidden(state, accountId) : false);
  const note = useSelector(state => accountId ? state.getIn(['relationships', accountId, 'note'], '') : '');
  const relationship = useSelector(state => accountId ? state.getIn(['relationships', accountId]) : null);
  const familiarFollowers = useSelector(state => accountId ? getAccountFamiliarFollowers(state, accountId) : null);
  const suspended = Boolean(account?.get('suspended'));
  const restricted = suspended || hidden;
  const isMutual = Boolean(relationship?.get('followed_by') && relationship?.get('following'));
  const isFollower = Boolean(relationship?.get('followed_by'));
  const showFamiliarFollowers = Boolean(relationship && familiarFollowers && !familiarFollowers.isEmpty() && !isFollower);

  useEffect(() => {
    if (accountId && !account) {
      dispatch(fetchAccount(accountId));
    }
  }, [account, accountId, dispatch]);

  useEffect(() => {
    if (accountId && account && !restricted) {
      dispatch(fetchAccountsFamiliarFollowers(accountId));
    }
  }, [account, accountId, dispatch, restricted]);

  return (
    <div ref={ref} id='hover-card' role='tooltip' className={`hover-card dropdown-animation${account ? '' : ' hover-card--loading'}`}>
      {account ? (
        restricted ? (
          <>
            <div className='account account--minimal'>
              <div className='account__wrapper'>
                <Link className='account__display-name' to={`/@${account.get('acct')}`}>
                  <div className='account__avatar-wrapper'>
                    <Avatar account={undefined} size={46} />
                  </div>
                  <div className='account__contents'>
                    <DisplayName account={account} />
                  </div>
                </Link>
              </div>
            </div>
            <div className='hover-card__content hover-card__limited-account-note'>
              {suspended ? (
                <FormattedMessage id='empty_column.account_suspended' defaultMessage='Account suspended' />
              ) : (
                <FormattedMessage id='limited_account_hint.title' defaultMessage='This profile has been hidden by the moderators of {domain}.' values={{ domain }} />
              )}
            </div>
          </>
        ) : (
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
              {(isMutual || isFollower) && (
                <span className='relationship-tag hover-card__relationship-tag'>
                  {isMutual ? (
                    <FormattedMessage id='account.mutual' defaultMessage='You follow each other' />
                  ) : (
                    <FormattedMessage id='account.follows_you' defaultMessage='Follows you' />
                  )}
                </span>
              )}
              {showFamiliarFollowers && (
                <div className='hover-card__familiar-followers'>
                  <FamiliarFollowersReadout familiarFollowers={familiarFollowers} />
                </div>
              )}
            </div>
          </>
        )
      ) : <LoadingIndicator />}
    </div>
  );
});

HoverCardAccount.displayName = 'HoverCardAccount';

HoverCardAccount.propTypes = {
  accountId: PropTypes.string,
};
