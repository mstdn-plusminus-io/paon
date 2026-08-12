import PropTypes from 'prop-types';

import { Link } from 'react-router-dom';

import { connect } from 'react-redux';

import { Avatar } from 'mastodon/components/avatar';

const mapStateToProps = (state, { accountId }) => ({
  account: state.getIn(['accounts', accountId]),
});

export const AuthorLinkComponent = ({ accountId, account }) => {
  if (!account) {
    return null;
  }

  return (
    <Link
      to={`/@${account.get('acct')}`}
      className='story__details__shared__author-link'
      data-hover-card-account={accountId}
    >
      <Avatar account={account} size={16} />
      <bdi dangerouslySetInnerHTML={{ __html: account.get('display_name_html') }} />
    </Link>
  );
};

AuthorLinkComponent.propTypes = {
  accountId: PropTypes.string.isRequired,
  account: PropTypes.object,
};

export const AuthorLink = connect(mapStateToProps)(AuthorLinkComponent);

