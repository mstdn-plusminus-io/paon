import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { defineMessages, injectIntl } from 'react-intl';

import { connect } from 'react-redux';

import { initMediaEditModal, submitCompose } from 'mastodon/actions/compose';

import ConfirmationModal from './confirmation_modal';

const messages = defineMessages({
  confirm: {
    id: 'confirmations.missing_alt_text.confirm',
    defaultMessage: 'Add alt text',
  },
  message: {
    id: 'confirmations.missing_alt_text.message',
    defaultMessage: 'Your post contains media without alt text. Adding descriptions helps make your content accessible to more people.',
  },
  secondary: {
    id: 'confirmations.missing_alt_text.secondary',
    defaultMessage: 'Post anyway',
  },
});

class MissingAltTextModal extends PureComponent {

  static propTypes = {
    id: PropTypes.string.isRequired,
    router: PropTypes.object,
    intl: PropTypes.object.isRequired,
    onClose: PropTypes.func.isRequired,
    onEdit: PropTypes.func.isRequired,
    onSubmit: PropTypes.func.isRequired,
  };

  handleEdit = () => {
    this.props.onEdit(this.props.id);
  };

  handleSubmit = () => {
    this.props.onSubmit(this.props.router);
  };

  render () {
    const { intl, onClose } = this.props;

    return (
      <ConfirmationModal
        message={intl.formatMessage(messages.message)}
        confirm={intl.formatMessage(messages.confirm)}
        secondary={intl.formatMessage(messages.secondary)}
        onConfirm={this.handleEdit}
        onSecondary={this.handleSubmit}
        onClose={onClose}
      />
    );
  }

}

const mapDispatchToProps = dispatch => ({
  onEdit: id => dispatch(initMediaEditModal(id)),
  onSubmit: router => dispatch(submitCompose(router)),
});

export default connect(null, mapDispatchToProps)(injectIntl(MissingAltTextModal));
