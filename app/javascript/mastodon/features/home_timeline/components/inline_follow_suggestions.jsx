import { useCallback, useEffect, useRef, useState } from 'react';

import { defineMessages, FormattedMessage, useIntl } from 'react-intl';

import { Link } from 'react-router-dom';

import { useDispatch, useSelector } from 'react-redux';

import { changeSetting } from 'mastodon/actions/settings';
import { fetchSuggestions } from 'mastodon/actions/suggestions';
import { FollowSuggestionCard } from 'mastodon/components/follow_suggestion_card';
import { Icon } from 'mastodon/components/icon';
import { bannerSettings } from 'mastodon/settings';

const messages = defineMessages({
  previous: { id: 'lightbox.previous', defaultMessage: 'Previous' },
  next: { id: 'lightbox.next', defaultMessage: 'Next' },
});

const DISMISSIBLE_ID = 'home/follow-suggestions';

export const InlineFollowSuggestions = () => {
  const intl = useIntl();
  const dispatch = useDispatch();
  const suggestions = useSelector(state => state.getIn(['suggestions', 'items']));
  const isLoading = useSelector(state => state.getIn(['suggestions', 'isLoading']));
  const dismissed = useSelector(state => state.getIn(['settings', 'dismissed_banners', DISMISSIBLE_ID], false));
  const bodyRef = useRef();
  const [canScrollLeft, setCanScrollLeft] = useState(false);
  const [canScrollRight, setCanScrollRight] = useState(false);

  const updateScrollButtons = useCallback(() => {
    const body = bodyRef.current;
    if (!body) {
      return;
    }

    if (getComputedStyle(body).direction === 'rtl') {
      setCanScrollLeft(body.clientWidth - body.scrollLeft < body.scrollWidth - 1);
      setCanScrollRight(body.scrollLeft < 0);
    } else {
      setCanScrollLeft(body.scrollLeft > 0);
      setCanScrollRight(body.scrollLeft + body.clientWidth < body.scrollWidth - 1);
    }
  }, []);

  useEffect(() => {
    dispatch(fetchSuggestions(true));
  }, [dispatch]);

  useEffect(updateScrollButtons, [suggestions, updateScrollButtons]);

  const handleDismiss = useCallback(() => {
    bannerSettings.set(DISMISSIBLE_ID, true);
    dispatch(changeSetting(['dismissed_banners', DISMISSIBLE_ID], true));
  }, [dispatch]);

  const handleScroll = useCallback(direction => {
    bodyRef.current?.scrollBy({ left: direction * 240, behavior: 'smooth' });
  }, []);
  const handleScrollLeft = useCallback(() => handleScroll(-1), [handleScroll]);
  const handleScrollRight = useCallback(() => handleScroll(1), [handleScroll]);

  if (dismissed || bannerSettings.get(DISMISSIBLE_ID) || (!isLoading && suggestions.isEmpty())) {
    return null;
  }

  return (
    <section className='inline-follow-suggestions' aria-labelledby='inline-follow-suggestions-title'>
      <div className='inline-follow-suggestions__header'>
        <h3 id='inline-follow-suggestions-title'><FormattedMessage id='follow_suggestions.who_to_follow' defaultMessage='Who to follow' /></h3>
        <div>
          <button type='button' className='link-button' onClick={handleDismiss}><FormattedMessage id='follow_suggestions.dismiss' defaultMessage="Don't show again" /></button>
          <Link to='/explore/suggestions' className='link-button'><FormattedMessage id='follow_suggestions.view_all' defaultMessage='View all' /></Link>
        </div>
      </div>
      <div className='inline-follow-suggestions__body'>
        {canScrollLeft && <button type='button' className='inline-follow-suggestions__scroll inline-follow-suggestions__scroll--left' aria-label={intl.formatMessage(messages.previous)} onClick={handleScrollLeft}><Icon id='chevron-left' /></button>}
        <div className='inline-follow-suggestions__cards' ref={bodyRef} onScroll={updateScrollButtons}>
          {suggestions.map(suggestion => <FollowSuggestionCard key={suggestion.get('account')} id={suggestion.get('account')} sources={suggestion.get('sources')} compact />)}
        </div>
        {canScrollRight && <button type='button' className='inline-follow-suggestions__scroll inline-follow-suggestions__scroll--right' aria-label={intl.formatMessage(messages.next)} onClick={handleScrollRight}><Icon id='chevron-right' /></button>}
      </div>
    </section>
  );
};
