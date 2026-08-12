import { shouldLoadMoreMedia } from '../utils';

describe('shouldLoadMoreMedia', () => {
  it('loads when the active scroll container approaches the bottom', () => {
    expect(shouldLoadMoreMedia({ scrollTop: 851, scrollHeight: 1200, clientHeight: 200, isLoading: false })).toBe(true);
  });

  it('does not load while another page is loading', () => {
    expect(shouldLoadMoreMedia({ scrollTop: 851, scrollHeight: 1200, clientHeight: 200, isLoading: true })).toBe(false);
  });

  it('does not load when the active scroll container is away from the bottom', () => {
    expect(shouldLoadMoreMedia({ scrollTop: 800, scrollHeight: 1200, clientHeight: 200, isLoading: false })).toBe(false);
  });
});
