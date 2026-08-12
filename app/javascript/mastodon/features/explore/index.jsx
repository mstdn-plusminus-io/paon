import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { defineMessages, injectIntl, FormattedMessage } from 'react-intl';

import { Helmet } from 'react-helmet';
import { NavLink, Switch, Route } from 'react-router-dom';

import { connect } from 'react-redux';

import { changeSearch, clearSearch, submitSearch } from 'mastodon/actions/search';
import Column from 'mastodon/components/column';
import ColumnHeader from 'mastodon/components/column_header';
import Search from 'mastodon/features/compose/containers/search_container';
import { identityContextPropShape, withIdentity } from 'mastodon/identity_context';
import { trendsEnabled } from 'mastodon/initial_state';
import { parseSearchQuery } from 'mastodon/utils/search_query';

import Links from './links';
import SearchResults from './results';
import Statuses from './statuses';
import Suggestions from './suggestions';
import Tags from './tags';

const messages = defineMessages({
  title: { id: 'explore.title', defaultMessage: 'Trending' },
  searchResults: { id: 'explore.search_results', defaultMessage: 'Search results' },
});

const mapStateToProps = (state, ownProps) => ({
  layout: state.getIn(['meta', 'layout']),
  isSearching: ownProps.location?.pathname === '/search' || state.getIn(['search', 'submitted']) || !trendsEnabled,
});

class Explore extends PureComponent {

  static contextTypes = {
    router: PropTypes.object,
  };

  static propTypes = {
    identity: identityContextPropShape,
    intl: PropTypes.object.isRequired,
    multiColumn: PropTypes.bool,
    isSearching: PropTypes.bool,
    dispatch: PropTypes.func.isRequired,
    location: PropTypes.object.isRequired,
  };

  componentDidMount () {
    this.syncSearchFromLocation();
  }

  componentDidUpdate (previousProps) {
    const { location } = this.props;

    if (location.pathname !== previousProps.location.pathname || location.search !== previousProps.location.search) {
      this.syncSearchFromLocation();
    }
  }

  syncSearchFromLocation = () => {
    const { dispatch, location } = this.props;

    if (location.pathname !== '/search') {
      return;
    }

    const { q, type } = parseSearchQuery(location.search);

    if (q.length === 0) {
      dispatch(clearSearch());
      return;
    }

    dispatch(changeSearch(q));
    dispatch(submitSearch(type, q));
  };

  handleHeaderClick = () => {
    this.column.scrollTop();
  };

  setRef = c => {
    this.column = c;
  };

  render() {
    const { intl, multiColumn, isSearching } = this.props;
    const { signedIn } = this.props.identity;

    return (
      <Column bindToDocument={!multiColumn} ref={this.setRef} label={intl.formatMessage(messages.title)}>
        <ColumnHeader
          icon={isSearching ? 'search' : 'hashtag'}
          title={intl.formatMessage(isSearching ? messages.searchResults : messages.title)}
          onClick={this.handleHeaderClick}
          multiColumn={multiColumn}
        />

        <div className='explore__search-header'>
          <Search openInRoute />
        </div>

        {isSearching ? (
          <SearchResults />
        ) : (
          <>
            <div className='account__section-headline'>
              <NavLink exact to='/explore'>
                <FormattedMessage tagName='div' id='explore.trending_statuses' defaultMessage='Posts' />
              </NavLink>

              <NavLink exact to='/explore/tags'>
                <FormattedMessage tagName='div' id='explore.trending_tags' defaultMessage='Hashtags' />
              </NavLink>

              {signedIn && (
                <NavLink exact to='/explore/suggestions'>
                  <FormattedMessage tagName='div' id='explore.suggested_follows' defaultMessage='People' />
                </NavLink>
              )}

              <NavLink exact to='/explore/links'>
                <FormattedMessage tagName='div' id='explore.trending_links' defaultMessage='News' />
              </NavLink>
            </div>

            <Switch>
              <Route path='/explore/tags' component={Tags} />
              <Route path='/explore/links' component={Links} />
              <Route path='/explore/suggestions' component={Suggestions} />
              <Route exact path={['/explore', '/explore/posts', '/search']}>
                <Statuses multiColumn={multiColumn} />
              </Route>
            </Switch>

            <Helmet>
              <title>{intl.formatMessage(messages.title)}</title>
              <meta name='robots' content={isSearching ? 'noindex' : 'all'} />
            </Helmet>
          </>
        )}
      </Column>
    );
  }

}

export default withIdentity(connect(mapStateToProps)(injectIntl(Explore)));
