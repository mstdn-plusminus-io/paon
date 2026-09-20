import PropTypes from 'prop-types';

import { FormattedMessage } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';
import { connect } from 'react-redux';

import InlineAccount from 'mastodon/components/inline_account';
import { makeGetAccount } from 'mastodon/selectors';

const makeMapStateToProps = () => {
  const getAccount = makeGetAccount();

  return (state, { accountId }) => ({
    account: getAccount(state, accountId),
  });
};

const MoreFromAuthorComponent = ({ accountId, account }) => {
  if (!account) {
    return null;
  }

  return (
    <div className='more-from-author'>
      <FormattedMessage
        id='link_preview.more_from_author'
        defaultMessage='More from {name}'
        values={{
          name: (
            <a href={`/@${account.get('acct')}`} data-hover-card-account={accountId} target='_blank' rel='noopener noreferrer'>
              <InlineAccount accountId={accountId} />
            </a>
          ),
        }}
      />
    </div>
  );
};

MoreFromAuthorComponent.propTypes = {
  accountId: PropTypes.string.isRequired,
  account: ImmutablePropTypes.map,
};

export const MoreFromAuthor = connect(makeMapStateToProps)(MoreFromAuthorComponent);
