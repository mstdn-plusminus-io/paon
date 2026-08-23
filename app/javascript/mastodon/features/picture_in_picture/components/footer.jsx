import PropTypes from 'prop-types';

import { defineMessages, injectIntl } from 'react-intl';

import classNames from 'classnames';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';
import { connect } from 'react-redux';

import { initBoostModal } from 'mastodon/actions/boosts';
import { replyCompose } from 'mastodon/actions/compose';
import { reblog, favourite, unreblog, unfavourite } from 'mastodon/actions/interactions';
import { openModal } from 'mastodon/actions/modal';
import { IconButton } from 'mastodon/components/icon_button';
import { fileNameFromURL } from 'mastodon/features/video';
import { identityContextPropShape, withIdentity } from 'mastodon/identity_context';
import { me, boostModal } from 'mastodon/initial_state';
import { makeGetStatus } from 'mastodon/selectors';
import { discardDraftModalProps, hasComposeDraft } from 'mastodon/utils/discard_draft';
import { favouriteActionTitle } from 'mastodon/utils/status_action_titles';

const messages = defineMessages({
  reply: { id: 'status.reply', defaultMessage: 'Reply' },
  replyAll: { id: 'status.replyAll', defaultMessage: 'Reply to thread' },
  reblog: { id: 'status.reblog', defaultMessage: 'Boost' },
  reblog_private: { id: 'status.reblog_private', defaultMessage: 'Share again with your followers' },
  cancel_reblog_private: { id: 'status.cancel_reblog_private', defaultMessage: 'Unboost' },
  cannot_reblog: { id: 'status.cannot_reblog', defaultMessage: 'This post cannot be boosted' },
  open: { id: 'status.open', defaultMessage: 'Expand this status' },
  download: { id: 'video.download', defaultMessage: 'Download file' },
});

const makeMapStateToProps = () => {
  const getStatus = makeGetStatus();

  const mapStateToProps = (state, { statusId }) => ({
    status: getStatus(state, { id: statusId }),
    askReplyConfirmation: hasComposeDraft(state),
    isEditing: Boolean(state.getIn(['compose', 'id'])),
  });

  return mapStateToProps;
};

class Footer extends ImmutablePureComponent {

  static contextTypes = {
    router: PropTypes.object,
  };

  static propTypes = {
    identity: identityContextPropShape,
    statusId: PropTypes.string.isRequired,
    status: ImmutablePropTypes.map.isRequired,
    intl: PropTypes.object.isRequired,
    dispatch: PropTypes.func.isRequired,
    askReplyConfirmation: PropTypes.bool,
    isEditing: PropTypes.bool,
    withOpenButton: PropTypes.bool,
    onClose: PropTypes.func,
    src: PropTypes.string,
    type: PropTypes.string,
    attachmentId: PropTypes.string,
  };

  _performReply = () => {
    const { dispatch, status, onClose } = this.props;
    const { router } = this.context;

    if (onClose) {
      onClose(true);
    }

    dispatch(replyCompose(status, router.history));
  };

  handleReplyClick = () => {
    const { dispatch, askReplyConfirmation, status, intl, isEditing } = this.props;
    const { signedIn } = this.props.identity;

    if (signedIn) {
      if (askReplyConfirmation) {
        dispatch(openModal({
          modalType: 'CONFIRM',
          modalProps: {
            ...discardDraftModalProps(intl, isEditing),
            onConfirm: this._performReply,
          },
        }));
      } else {
        this._performReply();
      }
    } else {
      dispatch(openModal({
        modalType: 'INTERACTION',
        modalProps: {
          type: 'reply',
          accountId: status.getIn(['account', 'id']),
          url: status.get('uri'),
        },
      }));
    }
  };

  handleFavouriteClick = () => {
    const { dispatch, status } = this.props;
    const { signedIn } = this.props.identity;

    if (signedIn) {
      if (status.get('favourited')) {
        dispatch(unfavourite(status));
      } else {
        dispatch(favourite(status));
      }
    } else {
      dispatch(openModal({
        modalType: 'INTERACTION',
        modalProps: {
          type: 'favourite',
          accountId: status.getIn(['account', 'id']),
          url: status.get('uri'),
        },
      }));
    }
  };

  _performReblog = (status, privacy) => {
    const { dispatch } = this.props;
    dispatch(reblog(status, privacy));
  };

  handleReblogClick = e => {
    const { dispatch, status } = this.props;
    const { signedIn } = this.props.identity;

    if (signedIn) {
      if (status.get('reblogged')) {
        dispatch(unreblog(status));
      } else if (e?.shiftKey || !boostModal) {
        this._performReblog(status);
      } else {
        dispatch(initBoostModal({ status, onReblog: this._performReblog }));
      }
    } else {
      dispatch(openModal({
        modalType: 'INTERACTION',
        modalProps: {
          type: 'reblog',
          accountId: status.getIn(['account', 'id']),
          url: status.get('uri'),
        },
      }));
    }
  };

  handleOpenClick = e => {
    const { router } = this.context;

    if (e.button !== 0 || !router) {
      return;
    }

    const { status, onClose } = this.props;

    if (onClose) {
      onClose();
    }

    router.history.push(`/@${status.getIn(['account', 'acct'])}/${status.get('id')}`);
  };

  handleDownloadClick = () => {
    const { src, attachmentId } = this.props;

    if (!src) return;

    // Use download_proxy for all downloads to avoid CORS issues
    if (attachmentId) {
      const downloadUrl = `${window.location.origin}/download_proxy/${attachmentId}/original`;

      // Create download link
      const element = document.createElement('a');
      element.href = downloadUrl;
      element.download = fileNameFromURL(src);
      element.style.display = 'none';

      document.body.appendChild(element);
      element.click();
      document.body.removeChild(element);
    } else {
      // Fallback to original URL if no attachment ID
      window.open(src, '_blank', 'noopener,noreferrer');
    }
  };

  render () {
    const { status, intl, withOpenButton } = this.props;

    const publicStatus  = ['public', 'unlisted'].includes(status.get('visibility'));
    const reblogPrivate = status.getIn(['account', 'id']) === me && status.get('visibility') === 'private';

    let replyIcon, replyTitle;

    if (status.get('in_reply_to_id', null) === null) {
      replyIcon = 'reply';
      replyTitle = intl.formatMessage(messages.reply);
    } else {
      replyIcon = 'reply-all';
      replyTitle = intl.formatMessage(messages.replyAll);
    }

    let reblogTitle = '';

    if (status.get('reblogged')) {
      reblogTitle = intl.formatMessage(messages.cancel_reblog_private);
    } else if (publicStatus) {
      reblogTitle = intl.formatMessage(messages.reblog);
    } else if (reblogPrivate) {
      reblogTitle = intl.formatMessage(messages.reblog_private);
    } else {
      reblogTitle = intl.formatMessage(messages.cannot_reblog);
    }

    const favouriteTitle = favouriteActionTitle(intl, status.get('favourited'));

    return (
      <div className='picture-in-picture__footer'>
        <IconButton className='status__action-bar-button' title={replyTitle} icon={status.get('in_reply_to_account_id') === status.getIn(['account', 'id']) ? 'reply' : replyIcon} onClick={this.handleReplyClick} counter={status.get('replies_count')} />
        <IconButton className={classNames('status__action-bar-button', { reblogPrivate })} disabled={!publicStatus && !reblogPrivate}  active={status.get('reblogged')} title={reblogTitle} icon='retweet' onClick={this.handleReblogClick} counter={status.get('reblogs_count')} />
        <IconButton className='status__action-bar-button star-icon' animate active={status.get('favourited')} title={favouriteTitle} icon='star' onClick={this.handleFavouriteClick} counter={status.get('favourites_count')} />
        <IconButton className='status__action-bar-button' title={intl.formatMessage(messages.download)} icon='download' onClick={this.handleDownloadClick} disabled={!this.props?.src} />
        {withOpenButton && <IconButton className='status__action-bar-button' title={intl.formatMessage(messages.open)} icon='external-link' onClick={this.handleOpenClick} href={`/@${status.getIn(['account', 'acct'])}/${status.get('id')}`} />}
      </div>
    );
  }

}

export default connect(makeMapStateToProps)(withIdentity(injectIntl(Footer)));
