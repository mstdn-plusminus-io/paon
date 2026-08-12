const OFFICIAL_QUOTE_STATES = new Set([
  'accepted',
  'deleted',
  'soft_deleted',
  'pending',
  'rejected',
  'revoked',
  'unauthorized',
]);

const readQuoteValue = (quote, key) => {
  if (!quote) {
    return null;
  }

  return typeof quote.get === 'function' ? quote.get(key) : quote[key];
};

/**
 * Adapt the Mastodon 4.4 REST quote entity to the normalized frontend shape.
 *
 * Full Status entities contain `quoted_status`, while shallow/nested Status
 * entities contain `quoted_status_id`. The Redux store only keeps the status
 * ID in the quote so the quoted status follows the same filtering and account
 * hydration path as every other status.
 * @param {object | null | undefined} quote Raw REST quote entity.
 * @returns {{ state: string, quoted_status: string | null } | null} Normalized quote.
 */
export const normalizeStatusQuote = quote => {
  if (!quote) {
    return null;
  }

  const quotedStatus = quote.quoted_status;
  const quotedStatusId = quotedStatus?.id ?? quote.quoted_status_id ?? null;

  return {
    state: quote.state,
    quoted_status: quotedStatusId,
  };
};

export const getQuotedStatusId = quote =>
  readQuoteValue(quote, 'quoted_status') ??
  readQuoteValue(quote, 'quoted_status_id');

export const getQuoteState = quote => {
  const state = readQuoteValue(quote, 'state');

  if (OFFICIAL_QUOTE_STATES.has(state)) {
    return state;
  }

  // A quote with an embedded status but no state is not a valid 4.4 entity.
  // Treat it as unavailable rather than accidentally bypassing authorization.
  return 'not_found';
};

export const isAcceptedQuote = quote => getQuoteState(quote) === 'accepted';

/**
 * Remove the ActivityPub fallback link once the structured quote is shown.
 * @param {string} html Emojified status HTML.
 * @returns {string} HTML without quote fallback nodes.
 */
export const stripQuoteFallback = html => {
  if (!html) {
    return html;
  }

  const wrapper = document.createElement('div');
  wrapper.innerHTML = html;
  wrapper.querySelectorAll('.quote-inline').forEach(element => element.remove());

  return wrapper.innerHTML;
};
