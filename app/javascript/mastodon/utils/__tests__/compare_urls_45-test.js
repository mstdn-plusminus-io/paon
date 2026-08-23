import { compareUrls } from '../compare_urls';

describe('Mastodon 4.5 mention URL comparison', () => {
  it('treats an omitted root path and an explicit slash as the same URL', () => {
    expect(compareUrls('https://example.com', 'https://example.com/')).toBe(true);
  });

  it('compares origin, path, and query while ignoring fragments', () => {
    expect(compareUrls('https://example.com/@alice?a=1#one', 'https://example.com/@alice?a=1#two')).toBe(true);
    expect(compareUrls('https://example.com/@alice?a=1', 'https://example.com/@alice?a=2')).toBe(false);
    expect(compareUrls('https://other.example/@alice', 'https://example.com/@alice')).toBe(false);
  });

  it('fails closed for malformed URLs', () => {
    expect(compareUrls('not a URL', 'https://example.com/')).toBe(false);
  });
});
