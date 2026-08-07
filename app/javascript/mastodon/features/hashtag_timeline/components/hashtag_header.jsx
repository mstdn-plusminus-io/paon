import PropTypes from 'prop-types';

import { defineMessages, injectIntl, FormattedMessage } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';

import Button from 'mastodon/components/button';
import { ShortNumber } from 'mastodon/components/short_number';

const messages = defineMessages({
  followHashtag: { id: 'hashtag.follow', defaultMessage: 'Follow hashtag' },
  unfollowHashtag: { id: 'hashtag.unfollow', defaultMessage: 'Unfollow hashtag' },
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

export const HashtagHeader = injectIntl(({ tag, intl, disabled, onClick }) => {
  if (!tag) {
    return null;
  }

  const history = tag.get('history') || [];
  const [uses, people] = history.reduce((arr, day) => [arr[0] + (Number(day.get('uses')) || 0), arr[1] + (Number(day.get('accounts')) || 0)], [0, 0]);
  const usesToday = Number(tag.getIn(['history', 0, 'uses'])) || 0;
  const dividingCircle = <span aria-hidden>{' · '}</span>;

  return (
    <div className='hashtag-header'>
      <div className='hashtag-header__header'>
        <h1>#{tag.get('name')}</h1>
        <Button onClick={onClick} text={intl.formatMessage(tag.get('following') ? messages.unfollowHashtag : messages.followHashtag)} disabled={disabled} />
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
});

HashtagHeader.propTypes = {
  tag: ImmutablePropTypes.map,
  disabled: PropTypes.bool,
  onClick: PropTypes.func,
  intl: PropTypes.object,
};
