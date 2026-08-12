import PropTypes from 'prop-types';

import { defineMessages, injectIntl, FormattedMessage } from 'react-intl';

import classNames from 'classnames';
import { Link } from 'react-router-dom';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';

import { EmptyAccount } from 'mastodon/components/empty_account';
import { ShortNumber } from 'mastodon/components/short_number';
import { VerifiedBadge } from 'mastodon/components/verified_badge';
import DropdownMenuContainer from 'mastodon/containers/dropdown_menu_container';

import { me } from '../initial_state';

import { buildAccountQuickMenu } from './account_quick_menu';
import { Avatar } from './avatar';
import Button from './button';
import { FollowersCounter } from './counters';
import { DisplayName } from './display_name';
import { IconButton } from './icon_button';
import { RelativeTimestamp } from './relative_timestamp';

const messages = defineMessages({
  follow: { id: 'account.follow', defaultMessage: 'Follow' },
  unfollow: { id: 'account.unfollow', defaultMessage: 'Unfollow' },
  cancel_follow_request: { id: 'account.cancel_follow_request', defaultMessage: 'Withdraw follow request' },
  unblock: { id: 'account.unblock_short', defaultMessage: 'Unblock' },
  unmute: { id: 'account.unmute_short', defaultMessage: 'Unmute' },
  mute_notifications: { id: 'account.mute_notifications_short', defaultMessage: 'Mute notifications' },
  unmute_notifications: { id: 'account.unmute_notifications_short', defaultMessage: 'Unmute notifications' },
  mute: { id: 'account.mute_short', defaultMessage: 'Mute' },
  block: { id: 'account.block_short', defaultMessage: 'Block' },
  more: { id: 'status.more', defaultMessage: 'More' },
  addToLists: { id: 'account.add_or_remove_from_list', defaultMessage: 'Add or Remove from lists' },
  openOriginalPage: { id: 'account.open_original_page', defaultMessage: 'Open original page' },
});

export class Account extends ImmutablePureComponent {

  static propTypes = {
    size: PropTypes.number,
    account: ImmutablePropTypes.map,
    onFollow: PropTypes.func.isRequired,
    onBlock: PropTypes.func.isRequired,
    onMute: PropTypes.func.isRequired,
    onMuteNotifications: PropTypes.func.isRequired,
    onAddToList: PropTypes.func,
    intl: PropTypes.object.isRequired,
    hidden: PropTypes.bool,
    minimal: PropTypes.bool,
    actionIcon: PropTypes.string,
    actionTitle: PropTypes.string,
    defaultAction: PropTypes.string,
    onActionClick: PropTypes.func,
    withBio: PropTypes.bool,
  };

  static defaultProps = {
    size: 46,
  };

  handleFollow = () => {
    this.props.onFollow(this.props.account);
  };

  handleBlock = () => {
    this.props.onBlock(this.props.account);
  };

  handleMute = () => {
    this.props.onMute(this.props.account);
  };

  handleMuteNotifications = () => {
    this.props.onMuteNotifications(this.props.account, true);
  };

  handleUnmuteNotifications = () => {
    this.props.onMuteNotifications(this.props.account, false);
  };

  handleAction = () => {
    this.props.onActionClick(this.props.account);
  };

  handleAddToList = () => {
    this.props.onAddToList?.(this.props.account);
  };

  render () {
    const { account, intl, hidden, withBio, onActionClick, actionIcon, actionTitle, defaultAction, size, minimal } = this.props;

    if (!account) {
      return <EmptyAccount size={size} minimal={minimal} />;
    }

    if (hidden) {
      return (
        <>
          {account.get('display_name')}
          {account.get('username')}
        </>
      );
    }

    let buttons;
    let dropdown;

    const quickMenu = buildAccountQuickMenu({
      defaultAction,
      accountUrl: account.get('url'),
      mutingNotifications: account.getIn(['relationship', 'muting_notifications']),
      labels: {
        addToLists: intl.formatMessage(messages.addToLists),
        openOriginalPage: intl.formatMessage(messages.openOriginalPage),
        muteNotifications: intl.formatMessage(messages.mute_notifications),
        unmuteNotifications: intl.formatMessage(messages.unmute_notifications),
      },
      actions: {
        addToLists: this.handleAddToList,
        muteNotifications: this.handleMuteNotifications,
        unmuteNotifications: this.handleUnmuteNotifications,
      },
    });

    if (quickMenu.length > 0) {
      dropdown = <DropdownMenuContainer items={quickMenu} icon='ellipsis-h' size={24} title={intl.formatMessage(messages.more)} />;
    }

    if (actionIcon && onActionClick) {
      buttons = <IconButton icon={actionIcon} title={actionTitle} onClick={this.handleAction} />;
    } else if (!actionIcon && account.get('id') !== me && account.get('relationship', null) !== null) {
      const following = account.getIn(['relationship', 'following']);
      const requested = account.getIn(['relationship', 'requested']);
      const blocking  = account.getIn(['relationship', 'blocking']);
      const muting  = account.getIn(['relationship', 'muting']);

      if (requested) {
        buttons = <Button text={intl.formatMessage(messages.cancel_follow_request)} onClick={this.handleFollow} />;
      } else if (blocking) {
        buttons = <Button text={intl.formatMessage(messages.unblock)} onClick={this.handleBlock} />;
      } else if (muting) {
        buttons = <Button text={intl.formatMessage(messages.unmute)} onClick={this.handleMute} />;
      } else if (defaultAction === 'mute') {
        buttons = <Button title={intl.formatMessage(messages.mute)} onClick={this.handleMute} />;
      } else if (defaultAction === 'block') {
        buttons = <Button text={intl.formatMessage(messages.block)} onClick={this.handleBlock} />;
      } else if (!account.get('moved') || following) {
        buttons = <Button text={intl.formatMessage(following ? messages.unfollow : messages.follow)} onClick={this.handleFollow} />;
      }
    }

    let muteTimeRemaining;

    if (account.get('mute_expires_at')) {
      muteTimeRemaining = <>· <RelativeTimestamp timestamp={account.get('mute_expires_at')} futureDate /></>;
    }

    let verification;

    const firstVerifiedField = account.get('fields').find(item => !!item.get('verified_at'));

    if (firstVerifiedField) {
      verification = <VerifiedBadge link={firstVerifiedField.get('value')} />;
    }

    return (
      <div className={classNames('account', { 'account--minimal': minimal })}>
        <div className='account__wrapper'>
          <Link key={account.get('id')} className='account__display-name' title={account.get('acct')} to={`/@${account.get('acct')}`} data-hover-card-account={account.get('id')}>
            <div className='account__avatar-wrapper'>
              <Avatar account={account} size={size} />
            </div>

            <div className='account__contents'>
              <DisplayName account={account} />
              {!minimal && (
                <div className='account__details'>
                  <ShortNumber value={account.get('followers_count')} renderer={FollowersCounter} /> {verification} {muteTimeRemaining}
                </div>
              )}
            </div>
          </Link>

          {!minimal && (
            <div className='account__relationship'>
              {dropdown}
              {buttons}
            </div>
          )}
        </div>

        {withBio && (account.get('note').length > 0 ? (
          <div
            className='account__note translate'
            dangerouslySetInnerHTML={{ __html: account.get('note_emojified') }}
          />
        ) : (
          <div className='account__note account__note--missing'><FormattedMessage id='account.no_bio' defaultMessage='No description provided.' /></div>
        ))}
      </div>
    );
  }

}

export default injectIntl(Account);
