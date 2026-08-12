import PropTypes from 'prop-types';

import { defineMessages, injectIntl, FormattedMessage } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';

import Button from 'mastodon/components/button';
import { ShortNumber } from 'mastodon/components/short_number';
import DropdownMenuContainer from 'mastodon/containers/dropdown_menu_container';
import { identityContextPropShape } from 'mastodon/identity_context';
import { PERMISSION_MANAGE_TAXONOMIES } from 'mastodon/permissions';

const messages = defineMessages({
  followHashtag: { id: 'hashtag.follow', defaultMessage: 'Follow hashtag' },
  unfollowHashtag: { id: 'hashtag.unfollow', defaultMessage: 'Unfollow hashtag' },
  adminModeration: { id: 'hashtag.admin_moderation', defaultMessage: 'Open moderation interface for #{name}' },
  feature: { id: 'hashtag.feature', defaultMessage: 'Feature on profile' },
  unfeature: { id: 'hashtag.unfeature', defaultMessage: "Don't feature on profile" },
});

const usesRenderer = (displayNumber, pluralReady) => (
  <FormattedMessage
    id='hashtag.counter_by_uses'
    defaultMessage='{count, plural, one {{counter} post} other {{counter} posts}}'
    values={{
      count: pluralReady,
      counter: <strong>{displayNumber}</strong>,
    }}
  />
);

const peopleRenderer = (displayNumber, pluralReady) => (
  <FormattedMessage
    id='hashtag.counter_by_accounts'
    defaultMessage='{count, plural, one {{counter} participant} other {{counter} participants}}'
    values={{
      count: pluralReady,
      counter: <strong>{displayNumber}</strong>,
    }}
  />
);

const usesTodayRenderer = (displayNumber, pluralReady) => (
  <FormattedMessage
    id='hashtag.counter_by_uses_today'
    defaultMessage='{count, plural, one {{counter} post} other {{counter} posts}} today'
    values={{
      count: pluralReady,
      counter: <strong>{displayNumber}</strong>,
    }}
  />
);

export const HashtagHeaderComponent = ({ tag, intl, identity, disabled, onClick, onFeature }) => {
  if (!tag) {
    return null;
  }

  const { signedIn, permissions } = identity;
  const menu = [];

  if (signedIn) {
    menu.push({
      text: intl.formatMessage(tag.get('featuring') ? messages.unfeature : messages.feature),
      action: onFeature,
    });

    if ((permissions & PERMISSION_MANAGE_TAXONOMIES) === PERMISSION_MANAGE_TAXONOMIES) {
      menu.push(null);
      menu.push({
        text: intl.formatMessage(messages.adminModeration, { name: tag.get('name') }),
        href: `/admin/tags/${tag.get('id')}`,
      });
    }
  }

  const history = tag.get('history') || [];
  const [uses, people] = history.reduce((arr, day) => [arr[0] + (Number(day.get('uses')) || 0), arr[1] + (Number(day.get('accounts')) || 0)], [0, 0]);
  const usesToday = Number(tag.getIn(['history', 0, 'uses'])) || 0;
  const dividingCircle = <span aria-hidden>{' · '}</span>;

  return (
    <div className='hashtag-header'>
      <div className='hashtag-header__header'>
        <h1>#{tag.get('name')}</h1>

        <div className='hashtag-header__header__buttons'>
          {menu.length > 0 && (
            <DropdownMenuContainer
              items={menu}
              icon='ellipsis-v'
              size={24}
              direction='right'
            />
          )}

          <Button onClick={onClick} text={intl.formatMessage(tag.get('following') ? messages.unfollowHashtag : messages.followHashtag)} disabled={disabled} />
        </div>
      </div>

      <div>
        <ShortNumber value={uses} renderer={usesRenderer} />
        {dividingCircle}
        <ShortNumber value={people} renderer={peopleRenderer} />
        {dividingCircle}
        <ShortNumber value={usesToday} renderer={usesTodayRenderer} />
      </div>
    </div>
  );
};

export const HashtagHeader = injectIntl(HashtagHeaderComponent);

HashtagHeaderComponent.propTypes = {
  tag: ImmutablePropTypes.map,
  identity: identityContextPropShape,
  disabled: PropTypes.bool,
  onClick: PropTypes.func,
  onFeature: PropTypes.func,
  intl: PropTypes.object,
};
