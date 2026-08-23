import { markStatusExternalLink } from '../status_external_link';

describe('Mastodon 4.5 external status links', () => {
  it('keeps opener isolation without unconditionally suppressing the referrer', () => {
    const link = document.createElement('a');
    link.href = 'https://news.example/article';
    link.rel = 'nofollow noopener noreferrer';

    markStatusExternalLink(link);

    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'nofollow noopener');
    expect(link.relList.contains('noreferrer')).toBe(false);
    expect(link).toHaveClass('unhandled-link');
  });
});
