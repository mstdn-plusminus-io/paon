import PropTypes from 'prop-types';

import { FormattedMessage, injectIntl } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';
import { connect } from 'react-redux';
import { createSelector } from 'reselect';

import { setupListAdder, resetListAdder } from '../../actions/lists';
import { LoadingIndicator } from '../../components/loading_indicator';
import NewListForm from '../lists/components/new_list_form';

import Account from './components/account';
import List from './components/list';
// hack

const getOrderedLists = createSelector([state => state.get('lists')], lists => {
  if (!lists) {
    return lists;
  }

  return lists.toList().filter(item => !!item).sort((a, b) => a.get('title').localeCompare(b.get('title')));
});

const mapStateToProps = state => ({
  listIds: getOrderedLists(state).map(list=>list.get('id')),
  isLoading: state.getIn(['listAdder', 'lists', 'isLoading']),
  loaded: state.getIn(['listAdder', 'lists', 'loaded']),
  error: state.getIn(['listAdder', 'lists', 'error']),
});

const mapDispatchToProps = dispatch => ({
  onInitialize: accountId => dispatch(setupListAdder(accountId)),
  onReset: () => dispatch(resetListAdder()),
});

class ListAdder extends ImmutablePureComponent {

  static propTypes = {
    accountId: PropTypes.string.isRequired,
    onClose: PropTypes.func.isRequired,
    intl: PropTypes.object.isRequired,
    onInitialize: PropTypes.func.isRequired,
    onReset: PropTypes.func.isRequired,
    listIds: ImmutablePropTypes.list.isRequired,
    isLoading: PropTypes.bool.isRequired,
    loaded: PropTypes.bool.isRequired,
    error: PropTypes.bool.isRequired,
  };

  componentDidMount () {
    const { onInitialize, accountId } = this.props;
    onInitialize(accountId);
  }

  componentWillUnmount () {
    const { onReset } = this.props;
    onReset();
  }

  render () {
    const { accountId, error, isLoading, listIds, loaded } = this.props;

    return (
      <div className='modal-root__modal list-adder'>
        <div className='list-adder__account'>
          <Account accountId={accountId} />
        </div>

        <NewListForm />


        <div className='list-adder__lists' aria-live='polite' aria-busy={isLoading}>
          {isLoading && listIds.isEmpty() && <LoadingIndicator />}
          {!isLoading && loaded && listIds.isEmpty() && !error && (
            <div className='list-adder__empty'><FormattedMessage id='empty_column.lists' defaultMessage="You don't have any lists yet. When you create one, it will show up here." /></div>
          )}
          {error && (
            <div className='list-adder__empty'><FormattedMessage id='lists.load_error' defaultMessage='The list could not be loaded. Please try again.' /></div>
          )}
          {listIds.map(ListId => <List key={ListId} listId={ListId} />)}
        </div>
      </div>
    );
  }

}

export default connect(mapStateToProps, mapDispatchToProps)(injectIntl(ListAdder));
