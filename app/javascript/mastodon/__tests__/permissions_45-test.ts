import { canViewFeed, PERMISSION_VIEW_FEEDS } from '../permissions';

describe('Mastodon 4.5 feed access', () => {
  it('allows public feeds without authentication', () => {
    expect(canViewFeed(false, 0, 'public')).toBe(true);
  });

  it('requires a signed-in account for authenticated feeds', () => {
    expect(canViewFeed(false, 0, 'authenticated')).toBe(false);
    expect(canViewFeed(true, 0, 'authenticated')).toBe(true);
  });

  it('allows disabled feeds only to roles with the view-feeds permission', () => {
    expect(canViewFeed(true, 0, 'disabled')).toBe(false);
    expect(canViewFeed(true, PERMISSION_VIEW_FEEDS, 'disabled')).toBe(true);
  });

  it('fails closed when the server omits an access value', () => {
    expect(canViewFeed(false, 0, undefined)).toBe(false);
  });
});
