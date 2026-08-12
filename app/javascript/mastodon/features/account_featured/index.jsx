import PropTypes from 'prop-types';

import { FormattedMessage } from 'react-intl';

import { List as ImmutableList } from 'immutable';
import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';
import { connect } from 'react-redux';

import { fetchAccount, lookupAccount } from 'mastodon/actions/accounts';
import { fetchFeaturedAccounts, fetchFeaturedTags } from 'mastodon/actions/featured_tags';
import Hashtag from 'mastodon/components/hashtag';
import AccountContainer from 'mastodon/containers/account_container';
import BundleColumnError from 'mastodon/features/ui/components/bundle_column_error';
import { normalizeForLookup } from 'mastodon/reducers/accounts_map';
import { getAccountHidden } from 'mastodon/selectors';

import ColumnBackButton from '../../components/column_back_button';
import { LoadingIndicator } from '../../components/loading_indicator';
import { TimelineHint } from '../../components/timeline_hint';
import HeaderContainer from '../account_timeline/containers/header_container';
import Column from '../ui/components/column';

const emptyList = ImmutableList();

const mapStateToProps = (state, { params: { acct, id } }) => {
  const accountId = id || state.getIn(['accounts_map', normalizeForLookup(acct)]);

  if (accountId === null) {
    return { accountId, isLoading: false };
  }

  return {
    accountId,
    isLoading: !accountId || state.getIn(['user_lists', 'featured_tags', accountId, 'isLoading'], false) || state.getIn(['user_lists', 'featured_accounts', accountId, 'isLoading'], false),
    featuredTags: accountId ? state.getIn(['user_lists', 'featured_tags', accountId, 'items'], emptyList) : emptyList,
    featuredAccountIds: accountId ? state.getIn(['user_lists', 'featured_accounts', accountId, 'items'], emptyList) : emptyList,
    suspended: accountId ? state.getIn(['accounts', accountId, 'suspended'], false) : false,
    hidden: accountId ? getAccountHidden(state, accountId) : false,
    blockedBy: accountId ? state.getIn(['relationships', accountId, 'blocked_by'], false) : false,
    remoteUrl: accountId ? state.getIn(['accounts', accountId, 'url']) : null,
    remote: accountId ? state.getIn(['accounts', accountId, 'acct']) !== state.getIn(['accounts', accountId, 'username']) : false,
  };
};

class AccountFeatured extends ImmutablePureComponent {

  static propTypes = {
    params: PropTypes.shape({
      acct: PropTypes.string,
      id: PropTypes.string,
    }).isRequired,
    accountId: PropTypes.string,
    dispatch: PropTypes.func.isRequired,
    featuredTags: ImmutablePropTypes.list,
    featuredAccountIds: ImmutablePropTypes.list,
    isLoading: PropTypes.bool,
    suspended: PropTypes.bool,
    hidden: PropTypes.bool,
    blockedBy: PropTypes.bool,
    remote: PropTypes.bool,
    remoteUrl: PropTypes.string,
    multiColumn: PropTypes.bool,
  };

  load = accountId => {
    const { dispatch } = this.props;
    dispatch(fetchAccount(accountId));
    dispatch(fetchFeaturedTags(accountId));
    dispatch(fetchFeaturedAccounts(accountId));
  };

  componentDidMount () {
    const { accountId, dispatch, params: { acct } } = this.props;
    if (accountId) {
      this.load(accountId);
    } else {
      dispatch(lookupAccount(acct));
    }
  }

  componentDidUpdate (prevProps) {
    const { accountId, dispatch, params: { acct } } = this.props;
    if (prevProps.accountId !== accountId && accountId) {
      this.load(accountId);
    } else if (prevProps.params.acct !== acct) {
      dispatch(lookupAccount(acct));
    }
  }

  renderEmpty () {
    const { accountId, suspended, hidden, blockedBy } = this.props;
    let message;
    if (suspended) {
      message = <FormattedMessage id='empty_column.account_suspended' defaultMessage='Account suspended' />;
    } else if (hidden || blockedBy) {
      message = <FormattedMessage id='empty_column.account_unavailable' defaultMessage='Profile unavailable' />;
    } else {
      message = <FormattedMessage id='account.featured.empty' defaultMessage='This profile has not featured any hashtags or profiles yet.' />;
    }

    return <div className='empty-column-indicator' data-account-id={accountId}>{message}</div>;
  }

  render () {
    const { accountId, featuredTags = emptyList, featuredAccountIds = emptyList, isLoading, suspended, hidden, blockedBy, remote, remoteUrl, multiColumn } = this.props;
    const unavailable = suspended || hidden || blockedBy;

    if (accountId === null) {
      return <BundleColumnError multiColumn={multiColumn} errorType='routing' />;
    }

    return (
      <Column>
        <ColumnBackButton multiColumn={multiColumn} />

        <div className='scrollable scrollable--flex'>
          {accountId && <HeaderContainer accountId={accountId} hideTabs={unavailable} />}

          {isLoading && !accountId ? <LoadingIndicator /> : null}
          {!isLoading && (unavailable || (featuredTags.isEmpty() && featuredAccountIds.isEmpty())) ? this.renderEmpty() : null}

          {!unavailable && !featuredTags.isEmpty() && (
            <section aria-labelledby='featured-hashtags-heading'>
              <h4 id='featured-hashtags-heading' className='column-subheading'><FormattedMessage id='account.featured.hashtags' defaultMessage='Hashtags' /></h4>
              {featuredTags.map(tag => (
                <Hashtag
                  key={tag.get('id') || tag.get('name')}
                  name={tag.get('name')}
                  to={`/@${this.props.params.acct}/tagged/${tag.get('name')}`}
                  uses={tag.get('statuses_count') * 1}
                  withGraph={false}
                />
              ))}
            </section>
          )}

          {!unavailable && !featuredAccountIds.isEmpty() && (
            <section aria-labelledby='featured-profiles-heading'>
              <h4 id='featured-profiles-heading' className='column-subheading'><FormattedMessage id='account.featured.accounts' defaultMessage='Profiles' /></h4>
              {featuredAccountIds.map(id => <AccountContainer key={id} id={id} />)}
            </section>
          )}

          {remote && remoteUrl ? <TimelineHint url={remoteUrl} resource={<FormattedMessage id='account.featured.remote_hint' defaultMessage='View the complete profile on the original server' />} /> : null}
        </div>
      </Column>
    );
  }

}

export default connect(mapStateToProps)(AccountFeatured);
