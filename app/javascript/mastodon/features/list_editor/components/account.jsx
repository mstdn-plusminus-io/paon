import PropTypes from 'prop-types';

import { defineMessages, FormattedMessage, injectIntl } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';
import { connect } from 'react-redux';

import { addToListEditor, followAndAddToListEditor, removeFromListEditor } from '../../../actions/lists';
import { openModal } from '../../../actions/modal';
import { Avatar } from '../../../components/avatar';
import { DisplayName } from '../../../components/display_name';
import { IconButton } from '../../../components/icon_button';
import { me } from '../../../initial_state';
import { makeGetAccount } from '../../../selectors';

const messages = defineMessages({
  remove: { id: 'lists.account.remove', defaultMessage: 'Remove from list' },
  add: { id: 'lists.account.add', defaultMessage: 'Add to list' },
  followAndAdd: { id: 'confirmations.follow_to_list.confirm', defaultMessage: 'Follow and add to list' },
});

const makeMapStateToProps = () => {
  const getAccount = makeGetAccount();

  const mapStateToProps = (state, { accountId, added }) => ({
    account: getAccount(state, accountId),
    added: typeof added === 'undefined' ? state.getIn(['listEditor', 'accounts', 'items']).includes(accountId) : added,
    following: accountId === me || state.getIn(['relationships', accountId, 'following'], false) || state.getIn(['relationships', accountId, 'requested'], false),
    pending: state.getIn(['listEditor', 'accounts', 'pending']).has(accountId),
  });

  return mapStateToProps;
};

const mapDispatchToProps = (dispatch, { accountId }) => ({
  onRemove: () => dispatch(removeFromListEditor(accountId)),
  onAdd: () => dispatch(addToListEditor(accountId)),
  onFollowAndAdd: (account, intl) => dispatch(openModal({
    modalType: 'CONFIRM',
    modalProps: {
      message: (
        <FormattedMessage
          id='confirmations.follow_to_list.message'
          defaultMessage='You need to be following {name} to add them to a list.'
          values={{ name: <strong>{account.get('display_name') || account.get('username')}</strong> }}
        />
      ),
      confirm: intl.formatMessage(messages.followAndAdd),
      onConfirm: () => dispatch(followAndAddToListEditor(accountId)),
    },
  })),
});

class Account extends ImmutablePureComponent {

  static propTypes = {
    account: ImmutablePropTypes.map.isRequired,
    intl: PropTypes.object.isRequired,
    onRemove: PropTypes.func.isRequired,
    onAdd: PropTypes.func.isRequired,
    onFollowAndAdd: PropTypes.func.isRequired,
    added: PropTypes.bool,
    following: PropTypes.bool.isRequired,
    pending: PropTypes.bool.isRequired,
  };

  static defaultProps = {
    added: false,
  };

  handleAdd = () => {
    const { account, following, intl, onAdd, onFollowAndAdd } = this.props;

    if (following) {
      onAdd();
    } else {
      onFollowAndAdd(account, intl);
    }
  };

  render () {
    const { account, intl, onRemove, added, pending } = this.props;

    let button;

    if (added) {
      button = <IconButton disabled={pending} icon='times' title={intl.formatMessage(messages.remove)} onClick={onRemove} />;
    } else {
      button = <IconButton disabled={pending} icon='plus' title={intl.formatMessage(messages.add)} onClick={this.handleAdd} />;
    }

    return (
      <div className='account'>
        <div className='account__wrapper'>
          <div className='account__display-name'>
            <div className='account__avatar-wrapper'><Avatar account={account} size={36} /></div>
            <DisplayName account={account} />
          </div>

          <div className='account__relationship'>
            {button}
          </div>
        </div>
      </div>
    );
  }

}

export default connect(makeMapStateToProps, mapDispatchToProps)(injectIntl(Account));
