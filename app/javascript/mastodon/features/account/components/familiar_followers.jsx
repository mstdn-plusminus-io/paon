import PropTypes from 'prop-types';

import { FormattedMessage } from 'react-intl';

import { Link } from 'react-router-dom';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';
import { connect } from 'react-redux';

import { fetchAccountsFamiliarFollowers } from 'mastodon/actions/accounts_familiar_followers';
import { Avatar } from 'mastodon/components/avatar';
import { getAccountFamiliarFollowers } from 'mastodon/selectors';

const AccountLink = ({ account }) => {
  if (!account) {
    return null;
  }

  return (
    <Link
      to={`/@${account.get('acct')}`}
      data-hover-card-account={account.get('id')}
      dangerouslySetInnerHTML={{ __html: account.get('display_name_html') }}
    />
  );
};

AccountLink.propTypes = {
  account: ImmutablePropTypes.map,
};

export const FamiliarFollowersReadout = ({ familiarFollowers }) => {
  const messageData = {
    name1: <AccountLink account={familiarFollowers.get(0)} />,
    name2: <AccountLink account={familiarFollowers.get(1)} />,
    othersCount: familiarFollowers.size - 2,
  };

  if (familiarFollowers.size === 1) {
    return <FormattedMessage id='account.familiar_followers_one' defaultMessage='Followed by {name1}' values={messageData} />;
  } else if (familiarFollowers.size === 2) {
    return <FormattedMessage id='account.familiar_followers_two' defaultMessage='Followed by {name1} and {name2}' values={messageData} />;
  }

  return <FormattedMessage id='account.familiar_followers_many' defaultMessage='Followed by {name1}, {name2}, and {othersCount, plural, one {one other you know} other {# others you know}}' values={messageData} />;
};

FamiliarFollowersReadout.propTypes = {
  familiarFollowers: ImmutablePropTypes.list.isRequired,
};

export const FamiliarFollowersView = ({ familiarFollowers, isLoading }) => {
  if (isLoading || !familiarFollowers || familiarFollowers.isEmpty()) {
    return null;
  }

  return (
    <div className='account__header__familiar-followers'>
      <div className='account__header__familiar-followers__avatars'>
        {familiarFollowers.slice(0, 3).map(account => {
          const displayName = account.get('display_name') || account.get('username');

          return (
            <Link
              key={account.get('id')}
              className='account__header__familiar-followers__avatar'
              to={`/@${account.get('acct')}`}
              aria-label={`${displayName} (@${account.get('acct')})`}
              data-hover-card-account={account.get('id')}
            >
              <Avatar account={account} size={28} />
            </Link>
          );
        }).toArray()}
      </div>

      <span><FamiliarFollowersReadout familiarFollowers={familiarFollowers} /></span>
    </div>
  );
};

FamiliarFollowersView.propTypes = {
  familiarFollowers: ImmutablePropTypes.list,
  isLoading: PropTypes.bool,
};

class FamiliarFollowers extends ImmutablePureComponent {

  static propTypes = {
    accountId: PropTypes.string.isRequired,
    familiarFollowers: ImmutablePropTypes.list,
    isLoading: PropTypes.bool.isRequired,
    onFetch: PropTypes.func.isRequired,
  };

  componentDidMount () {
    this.props.onFetch(this.props.accountId);
  }

  componentDidUpdate (prevProps) {
    if (prevProps.accountId !== this.props.accountId) {
      this.props.onFetch(this.props.accountId);
    }
  }

  render () {
    const { familiarFollowers, isLoading } = this.props;

    return <FamiliarFollowersView familiarFollowers={familiarFollowers} isLoading={isLoading} />;
  }

}

const mapStateToProps = (state, { accountId }) => ({
  familiarFollowers: getAccountFamiliarFollowers(state, accountId),
  isLoading: state.getIn(['accounts_familiar_followers', accountId, 'isLoading'], true),
});

const mapDispatchToProps = {
  onFetch: fetchAccountsFamiliarFollowers,
};

export default connect(mapStateToProps, mapDispatchToProps)(FamiliarFollowers);
