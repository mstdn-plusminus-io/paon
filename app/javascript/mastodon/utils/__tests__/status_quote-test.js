import { fromJS } from 'immutable';

import {
  getQuotedStatusId,
  getQuoteState,
  isAcceptedQuote,
  normalizeStatusQuote,
  stripQuoteFallback,
} from '../status_quote';

describe('Mastodon 4.4 quote adapter', () => {
  it('normalizes a full quote entity with an embedded status', () => {
    expect(normalizeStatusQuote({
      state: 'accepted',
      quoted_status: { id: '42', content: 'quoted' },
    })).toEqual({
      state: 'accepted',
      quoted_status: '42',
    });
  });

  it('normalizes a shallow quote entity with quoted_status_id', () => {
    expect(normalizeStatusQuote({
      state: 'accepted',
      quoted_status_id: '43',
    })).toEqual({
      state: 'accepted',
      quoted_status: '43',
    });
  });

  it.each(['accepted', 'pending', 'rejected', 'revoked', 'deleted', 'soft_deleted', 'unauthorized'])('preserves the supported %s state', quoteState => {
    const quote = fromJS({ state: quoteState, quoted_status: '42' });

    expect(getQuoteState(quote)).toBe(quoteState);
    expect(getQuotedStatusId(quote)).toBe('42');
    expect(isAcceptedQuote(quote)).toBe(quoteState === 'accepted');
  });

  it('fails closed for malformed quote state', () => {
    expect(getQuoteState(fromJS({ state: 'anything', quoted_status: '42' }))).toBe('not_found');
  });

  it('removes only the structured quote fallback link', () => {
    expect(stripQuoteFallback('<p>Hello</p><p class="quote-inline"><a href="https://example.com/@a/42">RE: https://example.com/@a/42</a></p><p>World</p>'))
      .toBe('<p>Hello</p><p>World</p>');
  });
});
