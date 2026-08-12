import PropTypes from 'prop-types';

import { FormattedMessage, injectIntl } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';
import { connect } from 'react-redux';

import spring from 'react-motion/lib/spring';

import { setupListEditor, clearListSuggestions, resetListEditor } from '../../actions/lists';
import { LoadingIndicator } from '../../components/loading_indicator';
import Motion from '../ui/util/optional_motion';

import Account from './components/account';
import EditListForm from './components/edit_list_form';
import Search from './components/search';

const mapStateToProps = state => ({
  accountIds: state.getIn(['listEditor', 'accounts', 'items']),
  accountsLoading: state.getIn(['listEditor', 'accounts', 'isLoading']),
  accountsLoaded: state.getIn(['listEditor', 'accounts', 'loaded']),
  accountsError: state.getIn(['listEditor', 'accounts', 'error']),
  searchAccountIds: state.getIn(['listEditor', 'suggestions', 'items']),
  searchValue: state.getIn(['listEditor', 'suggestions', 'value']),
  searchLoading: state.getIn(['listEditor', 'suggestions', 'isLoading']),
  searchLoaded: state.getIn(['listEditor', 'suggestions', 'loaded']),
  searchError: state.getIn(['listEditor', 'suggestions', 'error']),
});

const mapDispatchToProps = dispatch => ({
  onInitialize: listId => dispatch(setupListEditor(listId)),
  onClear: () => dispatch(clearListSuggestions()),
  onReset: () => dispatch(resetListEditor()),
});

class ListEditor extends ImmutablePureComponent {

  static propTypes = {
    listId: PropTypes.string.isRequired,
    onClose: PropTypes.func.isRequired,
    intl: PropTypes.object.isRequired,
    onInitialize: PropTypes.func.isRequired,
    onClear: PropTypes.func.isRequired,
    onReset: PropTypes.func.isRequired,
    accountIds: ImmutablePropTypes.list.isRequired,
    searchAccountIds: ImmutablePropTypes.list.isRequired,
    accountsLoading: PropTypes.bool.isRequired,
    accountsLoaded: PropTypes.bool.isRequired,
    accountsError: PropTypes.bool.isRequired,
    searchValue: PropTypes.string.isRequired,
    searchLoading: PropTypes.bool.isRequired,
    searchLoaded: PropTypes.bool.isRequired,
    searchError: PropTypes.bool.isRequired,
  };

  componentDidMount () {
    const { onInitialize, listId } = this.props;
    onInitialize(listId);
  }

  componentWillUnmount () {
    const { onReset } = this.props;
    onReset();
  }

  render () {
    const {
      accountIds,
      accountsError,
      accountsLoaded,
      accountsLoading,
      searchAccountIds,
      searchError,
      searchLoaded,
      searchLoading,
      searchValue,
      onClear,
    } = this.props;
    const showSearch = searchValue.trim().length > 0;

    return (
      <div className='modal-root__modal list-editor'>
        <EditListForm />

        <Search />

        <div className='drawer__pager'>
          <div className='drawer__inner list-editor__accounts' aria-live='polite' aria-busy={accountsLoading}>
            {accountsLoading && accountIds.isEmpty() && <LoadingIndicator />}
            {!accountsLoading && accountsLoaded && accountIds.isEmpty() && !accountsError && (
              <div className='list-editor__empty'><FormattedMessage id='lists.no_members_yet' defaultMessage='No members yet.' /></div>
            )}
            {accountsError && (
              <div className='list-editor__empty'><FormattedMessage id='lists.load_error' defaultMessage='The list could not be loaded. Please try again.' /></div>
            )}
            {accountIds.map(accountId => <Account key={accountId} accountId={accountId} added />)}
          </div>

          {showSearch && <button type='button' tabIndex={-1} aria-label={this.props.intl.formatMessage({ id: 'lists.search_clear', defaultMessage: 'Clear search' })} className='drawer__backdrop' onClick={onClear} />}

          <Motion defaultStyle={{ x: -100 }} style={{ x: spring(showSearch ? 0 : -100, { stiffness: 210, damping: 20 }) }}>
            {({ x }) => (
              <div className='drawer__inner backdrop' aria-live='polite' aria-busy={searchLoading} style={{ transform: x === 0 ? null : `translateX(${x}%)`, visibility: x === -100 ? 'hidden' : 'visible' }}>
                {searchLoading && <LoadingIndicator />}
                {!searchLoading && searchLoaded && searchAccountIds.isEmpty() && !searchError && (
                  <div className='list-editor__empty'><FormattedMessage id='lists.no_results_found' defaultMessage='No results found.' /></div>
                )}
                {searchError && (
                  <div className='list-editor__empty'><FormattedMessage id='lists.search_error' defaultMessage='Search failed. Please try again.' /></div>
                )}
                {searchAccountIds.map(accountId => <Account key={accountId} accountId={accountId} />)}
              </div>
            )}
          </Motion>
        </div>
      </div>
    );
  }

}

export default connect(mapStateToProps, mapDispatchToProps)(injectIntl(ListEditor));
