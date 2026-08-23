import { handleIframeUrl } from '../card';

describe('Mastodon 4.5 preview-card embeds', () => {
  it('passes a YouTube start time and the required referrer policy to the iframe', () => {
    const html = handleIframeUrl(
      '<iframe src="https://www.youtube.com/embed/video"></iframe>',
      'https://www.youtube.com/watch?v=video&t=37',
      'YouTube',
    );
    const document = new DOMParser().parseFromString(html, 'text/html');
    const iframe = document.querySelector('iframe');
    const url = new URL(iframe.src);

    expect(url.searchParams.get('start')).toBe('37');
    expect(url.searchParams.get('autoplay')).toBe('1');
    expect(iframe.getAttribute('referrerpolicy')).toBe('strict-origin-when-cross-origin');
  });
});
