import { publicLandingPath } from '../landing_page';

describe('Mastodon 4.5 public landing page', () => {
  it('uses trends only when trends are available', () => {
    expect(publicLandingPath({ trendsEnabled: true, landingPage: 'trends' })).toBe('/explore');
    expect(publicLandingPath({ trendsEnabled: false, landingPage: 'trends' })).toBe('/about');
  });

  it('uses the local feed only when it is public', () => {
    expect(publicLandingPath({ landingPage: 'local_feed', localLiveFeedAccess: 'public' })).toBe('/public/local');
    expect(publicLandingPath({ landingPage: 'local_feed', localLiveFeedAccess: 'authenticated' })).toBe('/about');
    expect(publicLandingPath({ landingPage: 'local_feed', localLiveFeedAccess: 'disabled' })).toBe('/about');
  });

  it('uses the about page by default', () => {
    expect(publicLandingPath({ landingPage: 'about' })).toBe('/about');
    expect(publicLandingPath({})).toBe('/about');
  });
});
