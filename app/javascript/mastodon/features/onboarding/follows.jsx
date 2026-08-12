import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { defineMessages, FormattedMessage, injectIntl } from 'react-intl';

import { Link } from 'react-router-dom';

import ImmutablePropTypes from 'react-immutable-proptypes';
import { connect } from 'react-redux';

import { debounce } from 'lodash';

import { fetchRelationships } from 'mastodon/actions/accounts';
import { importFetchedAccounts } from 'mastodon/actions/importer';
import { fetchSuggestions } from 'mastodon/actions/suggestions';
import { markAsPartial } from 'mastodon/actions/timelines';
import api from 'mastodon/api';
import Column from 'mastodon/components/column';
import ColumnBackButton from 'mastodon/components/column_back_button';
import { EmptyAccount } from 'mastodon/components/empty_account';
import { Icon } from 'mastodon/components/icon';
import Account from 'mastodon/containers/account_container';

const messages = defineMessages({
  search: { id: 'onboarding.follows.search', defaultMessage: 'Search for people' },
  clear: { id: 'onboarding.follows.search_clear', defaultMessage: 'Clear search' },
});

const mapStateToProps = state => ({
  suggestions: state.getIn(['suggestions', 'items']),
  isLoading: state.getIn(['suggestions', 'isLoading']),
});

class Follows extends PureComponent {

  static propTypes = {
    onBack: PropTypes.func,
    onComplete: PropTypes.func.isRequired,
    dispatch: PropTypes.func.isRequired,
    suggestions: ImmutablePropTypes.list,
    isLoading: PropTypes.bool,
    intl: PropTypes.object.isRequired,
    multiColumn: PropTypes.bool,
  };

  state = {
    searchValue: '',
    searchAccountIds: [],
    isLoadingSearch: false,
  };

  searchGeneration = 0;

  searchAccounts = debounce((value, generation) => {
    api().get('/api/v1/accounts/search', { params: { q: value } }).then(({ data }) => {
      if (generation !== this.searchGeneration) {
        return;
      }

      const accountIds = data.map(account => account.id);
      this.props.dispatch(importFetchedAccounts(data));
      this.props.dispatch(fetchRelationships(accountIds));
      this.setState({ searchAccountIds: accountIds, isLoadingSearch: false });
    }).catch(() => {
      if (generation === this.searchGeneration) {
        this.setState({ searchAccountIds: [], isLoadingSearch: false });
      }
    });
  }, 500);

  componentDidMount () {
    const { dispatch } = this.props;
    dispatch(fetchSuggestions(true));
  }

  componentWillUnmount () {
    const { dispatch } = this.props;
    this.searchAccounts.cancel();
    dispatch(markAsPartial('home'));
  }

  handleSearchChange = event => {
    const searchValue = event.target.value;
    const generation = ++this.searchGeneration;

    if (searchValue.trim().length === 0) {
      this.searchAccounts.cancel();
      this.setState({ searchValue, searchAccountIds: [], isLoadingSearch: false });
      return;
    }

    this.setState({ searchValue, isLoadingSearch: true });
    this.searchAccounts(searchValue.trim(), generation);
  };

  handleClearSearch = () => {
    this.searchGeneration += 1;
    this.searchAccounts.cancel();
    this.setState({ searchValue: '', searchAccountIds: [], isLoadingSearch: false });
  };

  handleSearchSubmit = event => {
    event.preventDefault();
  };

  render () {
    const { onBack, onComplete, isLoading, suggestions, intl, multiColumn } = this.props;
    const { searchValue, searchAccountIds, isLoadingSearch } = this.state;
    const isSearching = searchValue.trim().length > 0;

    let loadedContent;

    if ((isLoading && !isSearching) || (isLoadingSearch && searchAccountIds.length === 0)) {
      loadedContent = (new Array(8)).fill().map((_, i) => <EmptyAccount key={i} />);
    } else if (isSearching && searchAccountIds.length === 0) {
      loadedContent = <div className='follow-recommendations__empty'><FormattedMessage id='lists.no_results_found' defaultMessage='No results found.' /></div>;
    } else if (!isSearching && suggestions.isEmpty()) {
      loadedContent = <div className='follow-recommendations__empty'><FormattedMessage id='onboarding.follows.empty' defaultMessage='Unfortunately, no results can be shown right now. You can try using search or browsing the explore page to find people to follow, or try again later.' /></div>;
    } else if (isSearching) {
      loadedContent = searchAccountIds.map(accountId => <Account id={accountId} key={accountId} withBio />);
    } else {
      loadedContent = suggestions.map(suggestion => <Account id={suggestion.get('account')} key={suggestion.get('account')} withBio />);
    }

    return (
      <Column>
        <ColumnBackButton multiColumn={multiColumn} onClick={onBack} />

        <div className='scrollable privacy-policy'>
          <div className='column-title'>
            <h3><FormattedMessage id='onboarding.follows.title' defaultMessage='Popular on Mastodon' /></h3>
            <p><FormattedMessage id='onboarding.follows.lead' defaultMessage='You curate your own home feed. The more people you follow, the more active and interesting it will be. These profiles may be a good starting point—you can always unfollow them later!' /></p>
          </div>

          <form className='list-editor__search search' role='search' onSubmit={this.handleSearchSubmit}>
            <label>
              <span style={{ display: 'none' }}>{intl.formatMessage(messages.search)}</span>
              <input
                className='search__input'
                type='search'
                value={searchValue}
                onChange={this.handleSearchChange}
                placeholder={intl.formatMessage(messages.search)}
                aria-label={intl.formatMessage(messages.search)}
              />
            </label>
            <button type='button' className='search__icon' disabled={!isSearching} onClick={this.handleClearSearch} aria-label={intl.formatMessage(messages.clear)}>
              <Icon id={isSearching ? 'times-circle' : 'search'} />
            </button>
          </form>

          <div className='follow-recommendations'>
            {loadedContent}
          </div>

          <p className='onboarding__lead'><FormattedMessage id='onboarding.tips.accounts_from_other_servers' defaultMessage='<strong>Did you know?</strong> Since Mastodon is decentralized, some profiles you come across will be hosted on servers other than yours. And yet you can interact with them seamlessly! Their server is in the second half of their username!' values={{ strong: chunks => <strong>{chunks}</strong> }} /></p>

          <div className='onboarding__footer'>
            <button className='link-button' onClick={onBack}><FormattedMessage id='onboarding.actions.back' defaultMessage='Take me back' /></button>
            <Link className='button' to='/home' onClick={onComplete}><FormattedMessage id='onboarding.follows.done' defaultMessage='Done' /></Link>
          </div>
        </div>
      </Column>
    );
  }

}

export default connect(mapStateToProps)(injectIntl(Follows));
