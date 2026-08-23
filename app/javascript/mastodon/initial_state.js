// @ts-check

/**
 * @typedef Emoji
 * @property {string} shortcode
 * @property {string} static_url
 * @property {string} url
 */

/**
 * @typedef AccountField
 * @property {string} name
 * @property {string} value
 * @property {string} verified_at
 */

/**
 * @typedef Account
 * @property {string} acct
 * @property {string} avatar
 * @property {string} avatar_static
 * @property {boolean} bot
 * @property {string} created_at
 * @property {boolean=} discoverable
 * @property {string} display_name
 * @property {Emoji[]} emojis
 * @property {AccountField[]} fields
 * @property {number} followers_count
 * @property {number} following_count
 * @property {boolean} group
 * @property {string} header
 * @property {string} header_static
 * @property {string} id
 * @property {string=} last_status_at
 * @property {boolean} locked
 * @property {string} note
 * @property {number} statuses_count
 * @property {string} url
 * @property {string} username
 */

/**
 * @typedef {[code: string, name: string, localName: string]} InitialStateLanguage
 */

/**
 * @typedef InitialStateMeta
 * @property {string} access_token
 * @property {boolean=} advanced_layout
 * @property {boolean} auto_play_gif
 * @property {boolean} activity_api_enabled
 * @property {string} admin
 * @property {boolean=} boost_modal
 * @property {boolean} crop_images
 * @property {boolean=} delete_modal
 * @property {boolean=} disable_swiping
 * @property {string=} disabled_account_id
 * @property {string} display_media
 * @property {string} domain
 * @property {boolean=} expand_spoilers
 * @property {'auto' | 'native' | 'twemoji'=} emoji_style
 * @property {boolean} limited_federation_mode
 * @property {string} locale
 * @property {string | null} mascot
 * @property {boolean=} missing_alt_text_modal
 * @property {string=} me
 * @property {string=} moved_to_account_id
 * @property {string=} owner
 * @property {boolean} profile_directory
 * @property {boolean} registrations_open
 * @property {boolean} reduce_motion
 * @property {string} repository
 * @property {boolean} search_enabled
 * @property {boolean} trends_enabled
 * @property {boolean} single_user_mode
 * @property {string} source_url
 * @property {string} streaming_api_base_url
 * @property {'public' | 'authenticated' | 'disabled'} local_live_feed_access
 * @property {'public' | 'authenticated' | 'disabled'} remote_live_feed_access
 * @property {'public' | 'authenticated'} local_topic_feed_access
 * @property {'public' | 'authenticated' | 'disabled'} remote_topic_feed_access
 * @property {boolean} terms_of_service_enabled
 * @property {string} title
 * @property {boolean} show_trends
 * @property {'about' | 'trends' | 'local_feed'} landing_page
 * @property {boolean} disable_hover_cards
 * @property {boolean} use_blurhash
 * @property {boolean=} use_pending_items
 * @property {string} version
 * @property {string} actual_version
 * @property {string} sso_redirect
 */

/**
 * @typedef Role
 * @property {string} id
 * @property {string} name
 * @property {string} permissions
 * @property {string} color
 * @property {boolean} highlighted
 */

/**
 * @typedef InitialState
 * @property {Record<string, Account>} accounts
 * @property {InitialStateLanguage[]} languages
 * @property {string[]} features
 * @property {boolean=} critical_updates_pending
 * @property {InitialStateMeta} meta
 * @property {Role?} role
 */

const element = document.getElementById('initial-state');
/** @type {InitialState | undefined} */
const initialState = element?.textContent && JSON.parse(element.textContent);

/** @type {string} */
const initialPath = document.querySelector("head meta[name=initialPath]")?.getAttribute("content") ?? '';
/** @type {boolean} */
export const hasMultiColumnPath = initialPath === '/'
  || initialPath === '/getting-started'
  || initialPath === '/home'
  || initialPath.startsWith('/deck');

/**
 * @template {keyof InitialStateMeta} K
 * @param {K} prop
 * @returns {InitialStateMeta[K] | undefined}
 */
const getMeta = (prop) => initialState?.meta && initialState.meta[prop];

export const activityApiEnabled = getMeta('activity_api_enabled');
export const autoPlayGif = getMeta('auto_play_gif');
export const boostModal = getMeta('boost_modal');
export const cropImages = getMeta('crop_images');
export const deleteModal = getMeta('delete_modal');
export const disableSwiping = getMeta('disable_swiping');
export const disabledAccountId = getMeta('disabled_account_id');
export const displayMedia = getMeta('display_media');
export const domain = getMeta('domain');
export const expandSpoilers = getMeta('expand_spoilers');
export const emojiStyle = getMeta('emoji_style') ?? 'auto';
export const forceSingleColumn = !getMeta('advanced_layout');
export const limitedFederationMode = getMeta('limited_federation_mode');
export const mascot = getMeta('mascot');
export const missingAltTextModal = getMeta('missing_alt_text_modal') ?? true;
export const me = getMeta('me');
export const movedToAccountId = getMeta('moved_to_account_id');
export const owner = getMeta('owner');
export const profile_directory = getMeta('profile_directory');
export const reduceMotion = getMeta('reduce_motion');
export const registrationsOpen = getMeta('registrations_open');
export const repository = getMeta('repository');
export const searchEnabled = getMeta('search_enabled');
export const trendsEnabled = getMeta('trends_enabled');
export const showTrends = getMeta('show_trends');
export const singleUserMode = getMeta('single_user_mode');
export const source_url = getMeta('source_url');
export const localLiveFeedAccess = getMeta('local_live_feed_access');
export const remoteLiveFeedAccess = getMeta('remote_live_feed_access');
export const localTopicFeedAccess = getMeta('local_topic_feed_access');
export const remoteTopicFeedAccess = getMeta('remote_topic_feed_access');
export const termsOfServiceEnabled = getMeta('terms_of_service_enabled');
export const title = getMeta('title');
export const landingPage = getMeta('landing_page');
export const disableHoverCards = getMeta('disable_hover_cards');
export const useBlurhash = getMeta('use_blurhash');
export const usePendingItems = getMeta('use_pending_items');
export const version = getMeta('actual_version');
export const languages = initialState?.languages;
export const criticalUpdatesPending = initialState?.critical_updates_pending;
// @ts-expect-error
export const statusPageUrl = getMeta('status_page_url');
export const sso_redirect = getMeta('sso_redirect');

/**
 * Return the access token directly from the immutable boot payload instead of
 * copying this credential into Redux or React context.
 * @returns {string | undefined}
 */
export function getAccessToken() {
  return getMeta('access_token');
}

export default initialState;
