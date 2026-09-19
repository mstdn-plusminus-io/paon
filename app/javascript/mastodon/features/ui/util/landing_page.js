/**
 * Resolve Mastodon 4.5's configurable landing page for signed-out visitors.
 * A configured destination must also be publicly available.
 * @param {object} options
 * @param {boolean | undefined} options.trendsEnabled
 * @param {'about' | 'trends' | 'local_feed' | 'overview' | undefined} options.landingPage
 * @param {'public' | 'authenticated' | 'disabled' | undefined} options.localLiveFeedAccess
 * @returns {'/about' | '/explore' | '/public/local' | '/overview'}
 */
export const publicLandingPath = ({
  trendsEnabled,
  landingPage,
  localLiveFeedAccess,
}) => {
  if (landingPage === 'overview') return '/overview';
  if (trendsEnabled && landingPage === 'trends') return '/explore';
  if (localLiveFeedAccess === 'public' && landingPage === 'local_feed') {
    return '/public/local';
  }

  return '/about';
};
