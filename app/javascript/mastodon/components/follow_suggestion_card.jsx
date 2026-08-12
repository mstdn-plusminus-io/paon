import PropTypes from 'prop-types';
import { useCallback } from 'react';

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';
import { useDispatch } from 'react-redux';

import { dismissSuggestion } from 'mastodon/actions/suggestions';
import Account from 'mastodon/containers/account_container';
import { domain } from 'mastodon/initial_state';

import { Icon } from './icon';
import { IconButton } from './icon_button';

const messages = defineMessages({
  dismiss: { id: 'follow_suggestions.dismiss', defaultMessage: "Don't show again" },
  friendsOfFriendsHint: { id: 'follow_suggestions.hints.friends_of_friends', defaultMessage: 'This profile is popular among the people you follow.' },
  similarToRecentlyFollowedHint: { id: 'follow_suggestions.hints.similar_to_recently_followed', defaultMessage: 'This profile is similar to the profiles you have most recently followed.' },
  featuredHint: { id: 'follow_suggestions.hints.featured', defaultMessage: 'This profile has been hand-picked by the {domain} team.' },
  mostFollowedHint: { id: 'follow_suggestions.hints.most_followed', defaultMessage: 'This profile is one of the most followed on {domain}.' },
  mostInteractionsHint: { id: 'follow_suggestions.hints.most_interactions', defaultMessage: 'This profile has recently been getting a lot of attention on {domain}.' },
});

const sourceDescription = (intl, source) => {
  switch (source) {
  case 'friends_of_friends':
    return {
      label: <FormattedMessage id='follow_suggestions.personalized_suggestion' defaultMessage='Personalized suggestion' />,
      hint: intl.formatMessage(messages.friendsOfFriendsHint),
    };
  case 'similar_to_recently_followed':
    return {
      label: <FormattedMessage id='follow_suggestions.personalized_suggestion' defaultMessage='Personalized suggestion' />,
      hint: intl.formatMessage(messages.similarToRecentlyFollowedHint),
    };
  case 'featured':
    return {
      label: <FormattedMessage id='follow_suggestions.curated_suggestion' defaultMessage='Staff pick' />,
      hint: intl.formatMessage(messages.featuredHint, { domain }),
    };
  case 'most_followed':
    return {
      label: <FormattedMessage id='follow_suggestions.popular_suggestion' defaultMessage='Popular suggestion' />,
      hint: intl.formatMessage(messages.mostFollowedHint, { domain }),
    };
  case 'most_interactions':
    return {
      label: <FormattedMessage id='follow_suggestions.popular_suggestion' defaultMessage='Popular suggestion' />,
      hint: intl.formatMessage(messages.mostInteractionsHint, { domain }),
    };
  default:
    return null;
  }
};

export const FollowSuggestionCard = ({ id, sources, compact = false }) => {
  const dispatch = useDispatch();
  const intl = useIntl();
  const source = sources?.get?.(0) ?? sources?.[0];
  const description = sourceDescription(intl, source);
  const handleDismiss = useCallback(() => dispatch(dismissSuggestion(id)), [dispatch, id]);

  return (
    <div className={`follow-suggestion-card${compact ? ' follow-suggestion-card--compact' : ''}`}>
      <div className='follow-suggestion-card__header'>
        {description ? (
          <span className='follow-suggestion-card__source' title={description.hint}>
            <Icon id='info-circle' fixedWidth /> {description.label}
          </span>
        ) : <span />}
        <IconButton icon='times' title={intl.formatMessage(messages.dismiss)} onClick={handleDismiss} />
      </div>
      <Account id={id} withBio={!compact} />
    </div>
  );
};

FollowSuggestionCard.propTypes = {
  id: PropTypes.string.isRequired,
  sources: ImmutablePropTypes.list,
  compact: PropTypes.bool,
};
