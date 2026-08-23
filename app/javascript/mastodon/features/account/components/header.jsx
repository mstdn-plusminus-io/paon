import PropTypes from 'prop-types';

import { defineMessages, injectIntl, FormattedMessage } from 'react-intl';

import classNames from 'classnames';
import { Helmet } from 'react-helmet';
import { NavLink } from 'react-router-dom';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';

import { Avatar } from 'mastodon/components/avatar';
import { Badge, AutomatedBadge, GroupBadge } from 'mastodon/components/badge';
import Button from 'mastodon/components/button';
import { FollowersCounter, FollowingCounter, StatusesCounter } from 'mastodon/components/counters';
import { FormattedDateWrapper } from 'mastodon/components/formatted_date';
import { Icon }  from 'mastodon/components/icon';
import { IconButton } from 'mastodon/components/icon_button';
import { ShortNumber } from 'mastodon/components/short_number';
import DropdownMenuContainer from 'mastodon/containers/dropdown_menu_container';
import { identityContextPropShape, withIdentity } from 'mastodon/identity_context';
import { autoPlayGif, me, domain } from 'mastodon/initial_state';
import { PERMISSION_MANAGE_USERS, PERMISSION_MANAGE_FEDERATION } from 'mastodon/permissions';
import { accountRelationshipTagKeys, PROFILE_AVATAR_SIZE, shouldShowFamiliarFollowers } from 'mastodon/utils/account_profile';

import AccountNoteContainer from '../containers/account_note_container';
import FollowRequestNoteContainer from '../containers/follow_request_note_container';

import FamiliarFollowers from './familiar_followers';

const messages = defineMessages({
  unfollow: { id: 'account.unfollow', defaultMessage: 'Unfollow' },
  follow: { id: 'account.follow', defaultMessage: 'Follow' },
  followBack: { id: 'account.follow_back', defaultMessage: 'Follow back' },
  followRequest: { id: 'account.follow_request', defaultMessage: 'Request to follow' },
  cancel_follow_request: { id: 'account.follow_request_cancel', defaultMessage: 'Cancel request' },
  requested: { id: 'account.requested', defaultMessage: 'Awaiting approval. Click to cancel follow request' },
  unblock: { id: 'account.unblock', defaultMessage: 'Unblock @{name}' },
  edit_profile: { id: 'account.edit_profile', defaultMessage: 'Edit profile' },
  linkVerifiedOn: { id: 'account.link_verified_on', defaultMessage: 'Ownership of this link was checked on {date}' },
  account_locked: { id: 'account.locked_info', defaultMessage: 'This account privacy status is set to locked. The owner manually reviews who can follow them.' },
  mention: { id: 'account.mention', defaultMessage: 'Mention @{name}' },
  direct: { id: 'account.direct', defaultMessage: 'Privately mention @{name}' },
  unmute: { id: 'account.unmute', defaultMessage: 'Unmute @{name}' },
  block: { id: 'account.block', defaultMessage: 'Block @{name}' },
  mute: { id: 'account.mute', defaultMessage: 'Mute @{name}' },
  report: { id: 'account.report', defaultMessage: 'Report @{name}' },
  share: { id: 'account.share', defaultMessage: 'Share @{name}\'s profile' },
  copyProfile: { id: 'account.copy', defaultMessage: 'Copy link to profile' },
  copiedProfile: { id: 'copypaste.copied', defaultMessage: 'Copied' },
  remoteDomain: { id: 'account.remote_domain', defaultMessage: 'This profile is hosted on {domain}' },
  media: { id: 'account.media', defaultMessage: 'Media' },
  blockDomain: { id: 'account.block_domain', defaultMessage: 'Block domain {domain}' },
  unblockDomain: { id: 'account.unblock_domain', defaultMessage: 'Unblock domain {domain}' },
  hideReblogs: { id: 'account.hide_reblogs', defaultMessage: 'Hide boosts from @{name}' },
  showReblogs: { id: 'account.show_reblogs', defaultMessage: 'Show boosts from @{name}' },
  enableNotifications: { id: 'account.enable_notifications', defaultMessage: 'Notify me when @{name} posts' },
  disableNotifications: { id: 'account.disable_notifications', defaultMessage: 'Stop notifying me when @{name} posts' },
  pins: { id: 'navigation_bar.pins', defaultMessage: 'Pinned posts' },
  preferences: { id: 'navigation_bar.preferences', defaultMessage: 'Preferences' },
  follow_requests: { id: 'navigation_bar.follow_requests', defaultMessage: 'Follow requests' },
  favourites: { id: 'navigation_bar.favourites', defaultMessage: 'Favorites' },
  lists: { id: 'navigation_bar.lists', defaultMessage: 'Lists' },
  followed_tags: { id: 'navigation_bar.followed_tags', defaultMessage: 'Followed hashtags' },
  blocks: { id: 'navigation_bar.blocks', defaultMessage: 'Blocked users' },
  domain_blocks: { id: 'navigation_bar.domain_blocks', defaultMessage: 'Blocked domains' },
  mutes: { id: 'navigation_bar.mutes', defaultMessage: 'Muted users' },
  endorse: { id: 'account.endorse', defaultMessage: 'Feature on profile' },
  unendorse: { id: 'account.unendorse', defaultMessage: 'Don\'t feature on profile' },
  add_or_remove_from_list: { id: 'account.add_or_remove_from_list', defaultMessage: 'Add or Remove from lists' },
  admin_account: { id: 'status.admin_account', defaultMessage: 'Open moderation interface for @{name}' },
  admin_domain: { id: 'status.admin_domain', defaultMessage: 'Open moderation interface for {domain}' },
  languages: { id: 'account.languages', defaultMessage: 'Change subscribed languages' },
  openOriginalPage: { id: 'account.open_original_page', defaultMessage: 'Open original page' },
  removeFromFollowers: { id: 'account.remove_from_followers', defaultMessage: 'Remove {name} from followers' },
});

const titleFromAccount = account => {
  const displayName = account.get('display_name');
  const acct = account.get('acct') === account.get('username') ? `${account.get('username')}@${domain}` : account.get('acct');
  const prefix = displayName.trim().length === 0 ? account.get('username') : displayName;

  return `${prefix} (@${acct})`;
};

const dateFormatOptions = {
  month: 'short',
  day: 'numeric',
  year: 'numeric',
  hour12: false,
  hour: '2-digit',
  minute: '2-digit',
};

class Header extends ImmutablePureComponent {

  static contextTypes = {
    router: PropTypes.object,
  };

  static propTypes = {
    identity: identityContextPropShape,
    account: ImmutablePropTypes.map,
    identity_props: ImmutablePropTypes.list,
    onFollow: PropTypes.func.isRequired,
    onBlock: PropTypes.func.isRequired,
    onMention: PropTypes.func.isRequired,
    onDirect: PropTypes.func.isRequired,
    onReblogToggle: PropTypes.func.isRequired,
    onRemoveFromFollowers: PropTypes.func.isRequired,
    onNotifyToggle: PropTypes.func.isRequired,
    onReport: PropTypes.func.isRequired,
    onMute: PropTypes.func.isRequired,
    onBlockDomain: PropTypes.func.isRequired,
    onUnblockDomain: PropTypes.func.isRequired,
    onEndorseToggle: PropTypes.func.isRequired,
    onAddToList: PropTypes.func.isRequired,
    onEditAccountNote: PropTypes.func.isRequired,
    onChangeLanguages: PropTypes.func.isRequired,
    onInteractionModal: PropTypes.func.isRequired,
    onOpenAvatar: PropTypes.func.isRequired,
    onOpenURL: PropTypes.func.isRequired,
    intl: PropTypes.object.isRequired,
    domain: PropTypes.string.isRequired,
    hidden: PropTypes.bool,
  };

  state = {
    copied: false,
  };

  setRef = c => {
    this.node = c;
  };

  openEditProfile = () => {
    window.open('/settings/profile', '_blank');
  };

  isStatusesPageActive = (match, location) => {
    if (!match) {
      return false;
    }

    return !location.pathname.match(/\/(followers|following)\/?$/);
  };

  handleMouseEnter = ({ currentTarget }) => {
    if (autoPlayGif) {
      return;
    }

    const emojis = currentTarget.querySelectorAll('.custom-emoji');

    for (var i = 0; i < emojis.length; i++) {
      let emoji = emojis[i];
      emoji.src = emoji.getAttribute('data-original');
    }
  };

  handleMouseLeave = ({ currentTarget }) => {
    if (autoPlayGif) {
      return;
    }

    const emojis = currentTarget.querySelectorAll('.custom-emoji');

    for (var i = 0; i < emojis.length; i++) {
      let emoji = emojis[i];
      emoji.src = emoji.getAttribute('data-static');
    }
  };

  handleAvatarClick = e => {
    if (e.button === 0 && !(e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      this.props.onOpenAvatar();
    }
  };

  handleShare = () => {
    const { account } = this.props;

    navigator.share({
      url: account.get('url'),
    }).catch((e) => {
      if (e.name !== 'AbortError') console.error(e);
    });
  };

  handleCopy = () => {
    const url = this.props.account.get('url');
    const finish = () => {
      this.setState({ copied: true });
      window.clearTimeout(this.copyTimeout);
      this.copyTimeout = window.setTimeout(() => this.setState({ copied: false }), 1500);
    };

    if (navigator.clipboard?.writeText) {
      navigator.clipboard.writeText(url).then(finish).catch(() => this.copyWithExecCommand(url, finish));
    } else {
      this.copyWithExecCommand(url, finish);
    }
  };

  copyWithExecCommand = (value, callback) => {
    const input = document.createElement('textarea');
    input.value = value;
    input.setAttribute('readonly', '');
    input.style.position = 'fixed';
    input.style.opacity = '0';
    document.body.appendChild(input);
    input.select();
    document.execCommand('copy');
    input.remove();
    callback();
  };

  handleHashtagClick = e => {
    const { router } = this.context;
    const value = e.currentTarget.textContent.replace(/^#/, '');

    if (router && e.button === 0 && !(e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      router.history.push(`/tags/${value}`);
    }
  };

  handleMentionClick = e => {
    const { router } = this.context;
    const { onOpenURL } = this.props;

    if (router && e.button === 0 && !(e.ctrlKey || e.metaKey)) {
      e.preventDefault();

      const link = e.currentTarget;

      onOpenURL(link.href, router.history, () => {
        window.location = link.href;
      });
    }
  };

  _attachLinkEvents () {
    const node = this.node;

    if (!node) {
      return;
    }

    const links = node.querySelectorAll('a');

    let link;

    for (var i = 0; i < links.length; ++i) {
      link = links[i];

      if (link.textContent[0] === '#' || (link.previousSibling && link.previousSibling.textContent && link.previousSibling.textContent[link.previousSibling.textContent.length - 1] === '#')) {
        link.addEventListener('click', this.handleHashtagClick, false);
      } else if (link.classList.contains('mention')) {
        link.addEventListener('click', this.handleMentionClick, false);
      }
    }
  }

  componentDidMount () {
    this._attachLinkEvents();
  }

  componentDidUpdate () {
    this._attachLinkEvents();
  }

  componentWillUnmount () {
    window.clearTimeout(this.copyTimeout);
  }

  render () {
    const { account, hidden, intl, domain } = this.props;
    const { signedIn, permissions } = this.props.identity;

    if (!account) {
      return null;
    }

    const suspended    = account.get('suspended');
    const isRemote     = account.get('acct') !== account.get('username');
    const remoteDomain = isRemote ? account.get('acct').split('@')[1] : null;

    let info        = [];
    let actionBtn   = '';
    let bellBtn     = '';
    let lockedIcon  = '';
    let menu        = [];

    accountRelationshipTagKeys(account.get('relationship'), me === account.get('id')).forEach(tag => {
      switch (tag) {
      case 'mutual':
        info.push(<span key={tag} className='relationship-tag'><FormattedMessage id='account.mutual' defaultMessage='You follow each other' /></span>);
        break;
      case 'followed_by':
        info.push(<span key={tag} className='relationship-tag'><FormattedMessage id='account.follows_you' defaultMessage='Follows you' /></span>);
        break;
      case 'requested_by':
        info.push(<span key={tag} className='relationship-tag'><FormattedMessage id='account.requests_to_follow_you' defaultMessage='Requests to follow you' /></span>);
        break;
      case 'blocking':
        info.push(<span key={tag} className='relationship-tag'><FormattedMessage id='account.blocking' defaultMessage='Blocking' /></span>);
        break;
      case 'muting':
        info.push(<span key={tag} className='relationship-tag'><FormattedMessage id='account.muting' defaultMessage='Muting' /></span>);
        break;
      case 'domain_blocking':
        info.push(<span key={tag} className='relationship-tag'><FormattedMessage id='account.domain_blocking' defaultMessage='Blocking domain' /></span>);
        break;
      }
    });

    if (account.getIn(['relationship', 'requested']) || account.getIn(['relationship', 'following'])) {
      bellBtn = <IconButton icon={account.getIn(['relationship', 'notifying']) ? 'bell' : 'bell-o'} size={24} active={account.getIn(['relationship', 'notifying'])} title={intl.formatMessage(account.getIn(['relationship', 'notifying']) ? messages.disableNotifications : messages.enableNotifications, { name: account.get('username') })} onClick={this.props.onNotifyToggle} />;
    }

    if (me !== account.get('id')) {
      if (signedIn && !account.get('relationship')) { // Wait until the relationship is loaded
        actionBtn = '';
      } else if (account.getIn(['relationship', 'requested'])) {
        actionBtn = <Button text={intl.formatMessage(messages.cancel_follow_request)} title={intl.formatMessage(messages.requested)} onClick={this.props.onFollow} />;
      } else if (account.getIn(['relationship', 'muting'])) {
        actionBtn = <Button text={intl.formatMessage(messages.unmute, { name: account.get('username') })} onClick={this.props.onMute} />;
      } else if (!account.getIn(['relationship', 'blocking'])) {
        let actionMessage = messages.follow;

        if (account.getIn(['relationship', 'following'])) {
          actionMessage = messages.unfollow;
        } else if (account.getIn(['relationship', 'followed_by']) && !account.get('locked')) {
          actionMessage = messages.followBack;
        } else if (account.get('locked')) {
          actionMessage = messages.followRequest;
        }

        actionBtn = <Button disabled={account.getIn(['relationship', 'blocked_by'])} className={classNames({ 'button--destructive': account.getIn(['relationship', 'following']) })} text={intl.formatMessage(actionMessage)} onClick={signedIn ? this.props.onFollow : this.props.onInteractionModal} />;
      } else if (account.getIn(['relationship', 'blocking'])) {
        actionBtn = <Button text={intl.formatMessage(messages.unblock, { name: account.get('username') })} onClick={this.props.onBlock} />;
      }
    } else {
      actionBtn = <Button text={intl.formatMessage(messages.edit_profile)} onClick={this.openEditProfile} />;
    }

    if (account.get('moved') && !account.getIn(['relationship', 'following'])) {
      actionBtn = '';
    }

    if (account.get('locked')) {
      lockedIcon = <Icon id='lock' title={intl.formatMessage(messages.account_locked)} />;
    }

    if (signedIn && account.get('id') !== me) {
      menu.push({ text: intl.formatMessage(messages.mention, { name: account.get('username') }), action: this.props.onMention });
      menu.push({ text: intl.formatMessage(messages.direct, { name: account.get('username') }), action: this.props.onDirect });
      menu.push(null);
    }

    if (isRemote) {
      menu.push({ text: intl.formatMessage(messages.openOriginalPage), href: account.get('url') });
    }

    if ('share' in navigator) {
      menu.push({ text: intl.formatMessage(messages.share, { name: account.get('username') }), action: this.handleShare });
    }
    menu.push({ text: intl.formatMessage(this.state.copied ? messages.copiedProfile : messages.copyProfile), action: this.handleCopy });
    menu.push(null);

    if (account.get('id') === me) {
      menu.push({ text: intl.formatMessage(messages.edit_profile), href: '/settings/profile' });
      menu.push({ text: intl.formatMessage(messages.preferences), href: '/settings/preferences' });
      menu.push({ text: intl.formatMessage(messages.pins), to: '/pinned' });
      menu.push(null);
      menu.push({ text: intl.formatMessage(messages.follow_requests), to: '/follow_requests' });
      menu.push({ text: intl.formatMessage(messages.favourites), to: '/favourites' });
      menu.push({ text: intl.formatMessage(messages.lists), to: '/lists' });
      menu.push({ text: intl.formatMessage(messages.followed_tags), to: '/followed_tags' });
      menu.push(null);
      menu.push({ text: intl.formatMessage(messages.mutes), to: '/mutes' });
      menu.push({ text: intl.formatMessage(messages.blocks), to: '/blocks' });
      menu.push({ text: intl.formatMessage(messages.domain_blocks), to: '/domain_blocks' });
    } else if (signedIn) {
      if (account.getIn(['relationship', 'following'])) {
        if (!account.getIn(['relationship', 'muting'])) {
          if (account.getIn(['relationship', 'showing_reblogs'])) {
            menu.push({ text: intl.formatMessage(messages.hideReblogs, { name: account.get('username') }), action: this.props.onReblogToggle });
          } else {
            menu.push({ text: intl.formatMessage(messages.showReblogs, { name: account.get('username') }), action: this.props.onReblogToggle });
          }

          menu.push({ text: intl.formatMessage(messages.languages), action: this.props.onChangeLanguages });
          menu.push(null);
        }

        menu.push({ text: intl.formatMessage(account.getIn(['relationship', 'endorsed']) ? messages.unendorse : messages.endorse), action: this.props.onEndorseToggle });
        menu.push({ text: intl.formatMessage(messages.add_or_remove_from_list), action: this.props.onAddToList });
        menu.push(null);
      }

      if (account.getIn(['relationship', 'followed_by'])) {
        menu.push({ text: intl.formatMessage(messages.removeFromFollowers, { name: account.get('username') }), action: this.props.onRemoveFromFollowers, dangerous: true });
      }

      if (account.getIn(['relationship', 'muting'])) {
        menu.push({ text: intl.formatMessage(messages.unmute, { name: account.get('username') }), action: this.props.onMute });
      } else {
        menu.push({ text: intl.formatMessage(messages.mute, { name: account.get('username') }), action: this.props.onMute, dangerous: true });
      }

      if (account.getIn(['relationship', 'blocking'])) {
        menu.push({ text: intl.formatMessage(messages.unblock, { name: account.get('username') }), action: this.props.onBlock });
      } else {
        menu.push({ text: intl.formatMessage(messages.block, { name: account.get('username') }), action: this.props.onBlock, dangerous: true });
      }

      menu.push({ text: intl.formatMessage(messages.report, { name: account.get('username') }), action: this.props.onReport, dangerous: true });
    }

    if (signedIn && isRemote) {
      menu.push(null);

      if (account.getIn(['relationship', 'domain_blocking'])) {
        menu.push({ text: intl.formatMessage(messages.unblockDomain, { domain: remoteDomain }), action: this.props.onUnblockDomain });
      } else {
        menu.push({ text: intl.formatMessage(messages.blockDomain, { domain: remoteDomain }), action: this.props.onBlockDomain, dangerous: true });
      }
    }

    if ((account.get('id') !== me && (permissions & PERMISSION_MANAGE_USERS) === PERMISSION_MANAGE_USERS) || (isRemote && (permissions & PERMISSION_MANAGE_FEDERATION) === PERMISSION_MANAGE_FEDERATION)) {
      menu.push(null);
      if ((permissions & PERMISSION_MANAGE_USERS) === PERMISSION_MANAGE_USERS) {
        menu.push({ text: intl.formatMessage(messages.admin_account, { name: account.get('username') }), href: `/admin/accounts/${account.get('id')}` });
      }
      if (isRemote && (permissions & PERMISSION_MANAGE_FEDERATION) === PERMISSION_MANAGE_FEDERATION) {
        menu.push({ text: intl.formatMessage(messages.admin_domain, { domain: remoteDomain }), href: `/admin/instances/${remoteDomain}` });
      }
    }

    const content         = { __html: account.get('note_emojified') };
    const displayNameHtml = { __html: account.get('display_name_html') };
    const fields          = account.get('fields');
    const isLocal         = account.get('acct').indexOf('@') === -1;
    const isIndexable     = !account.get('noindex');

    const badges = [];

    if (account.get('bot')) {
      badges.push(<AutomatedBadge key='bot-badge' />);
    } else if (account.get('group')) {
      badges.push(<GroupBadge key='group-badge' />);
    }

    account.get('roles', []).forEach((role) => {
      badges.push(<Badge key={`role-badge-${role.get('id')}`} label={<span>{role.get('name')}</span>} domain={domain} />);
    });

    return (
      <div className={classNames('account__header', { inactive: !!account.get('moved') })} onMouseEnter={this.handleMouseEnter} onMouseLeave={this.handleMouseLeave}>
        {!(suspended || hidden || account.get('moved')) && account.getIn(['relationship', 'requested_by']) && <FollowRequestNoteContainer account={account} />}

        <div className='account__header__image'>
          <div className='account__header__info'>
            {!suspended && info}
          </div>

          {!(suspended || hidden) && <img src={autoPlayGif ? account.get('header') : account.get('header_static')} alt='' className='parallax' />}
        </div>

        <div className='account__header__bar'>
          <div className='account__header__tabs'>
            <a className='avatar' href={account.get('avatar')} rel='noopener noreferrer' target='_blank' onClick={this.handleAvatarClick}>
              <Avatar account={suspended || hidden ? undefined : account} size={PROFILE_AVATAR_SIZE} />
            </a>

            {!suspended && (
              <div className='account__header__tabs__buttons'>
                {!hidden && (
                  <>
                    {actionBtn}
                    {bellBtn}
                  </>
                )}

                <DropdownMenuContainer disabled={menu.length === 0} items={menu} icon='ellipsis-v' size={24} direction='right' />
              </div>
            )}
          </div>

          <div className='account__header__tabs__name'>
            <h1>
              <span dangerouslySetInnerHTML={displayNameHtml} />
              <small>
                <span>@{account.get('username')}</span>{'@'}<span className='account__header__domain' title={intl.formatMessage(messages.remoteDomain, { domain: isRemote ? remoteDomain : domain })}>{isRemote ? remoteDomain : domain}</span> {lockedIcon}
              </small>
            </h1>
          </div>

          {badges.length > 0 && (
            <div className='account__header__badges'>
              {badges}
            </div>
          )}

          {shouldShowFamiliarFollowers({
            accountId: account.get('id'),
            currentAccountId: me,
            signedIn,
            suspended,
            hidden,
          }) && <FamiliarFollowers accountId={account.get('id')} />}

          {!(suspended || hidden) && (
            <div className='account__header__extra'>
              <div className='account__header__bio' ref={this.setRef}>
                {(account.get('id') !== me && signedIn) && <AccountNoteContainer account={account} />}

                {account.get('note').length > 0 && account.get('note') !== '<p></p>' && <div className='account__header__content translate' dangerouslySetInnerHTML={content} />}

                <div className='account__header__fields'>
                  <dl>
                    <dt><FormattedMessage id='account.joined_short' defaultMessage='Joined' /></dt>
                    <dd><FormattedDateWrapper value={account.get('created_at')} year='numeric' month='short' day='2-digit' /></dd>
                  </dl>

                  {fields.map((pair, i) => (
                    <dl key={i} className={classNames({ verified: pair.get('verified_at') })}>
                      <dt dangerouslySetInnerHTML={{ __html: pair.get('name_emojified') }} title={pair.get('name')} className='translate' />

                      <dd className='translate' title={pair.get('value_plain')}>
                        {pair.get('verified_at') && <span title={intl.formatMessage(messages.linkVerifiedOn, { date: intl.formatDate(pair.get('verified_at'), dateFormatOptions) })}><Icon id='check' className='verified__mark' /></span>} <span dangerouslySetInnerHTML={{ __html: pair.get('value_emojified') }} />
                      </dd>
                    </dl>
                  ))}
                </div>
              </div>

              <div className='account__header__extra__links account__header__extra__links--responsive'>
                <NavLink isActive={this.isStatusesPageActive} activeClassName='active' to={`/@${account.get('acct')}`} title={intl.formatNumber(account.get('statuses_count'))}>
                  <ShortNumber
                    value={account.get('statuses_count')}
                    renderer={StatusesCounter}
                  />
                </NavLink>

                <NavLink exact activeClassName='active' to={`/@${account.get('acct')}/following`} title={intl.formatNumber(account.get('following_count'))}>
                  <ShortNumber
                    value={account.get('following_count')}
                    renderer={FollowingCounter}
                  />
                </NavLink>

                <NavLink exact activeClassName='active' to={`/@${account.get('acct')}/followers`} title={intl.formatNumber(account.get('followers_count'))}>
                  <ShortNumber
                    value={account.get('followers_count')}
                    renderer={FollowersCounter}
                  />
                </NavLink>
              </div>
            </div>
          )}
        </div>

        <Helmet>
          <title>{titleFromAccount(account)}</title>
          <meta name='robots' content={(isLocal && isIndexable) ? 'all' : 'noindex'} />
          <link rel='canonical' href={account.get('url')} />
        </Helmet>
      </div>
    );
  }

}

export default withIdentity(injectIntl(Header));
