import { columnNumberForLayout, focusAdjacentFeedItem } from '../feed_keyboard_navigation';

describe('Mastodon 4.5 feed keyboard navigation', () => {
  it('skips empty entries and can focus a follow-suggestions feed item', () => {
    document.body.innerHTML = `
      <div class="scrollable"><div class="item-list">
        <article><button class="focusable">First</button></article>
        <article></article>
        <article tabindex="-1"><section class="inline-follow-suggestions">Suggestions</section></article>
      </div></div>`;
    const container = document.querySelector('.scrollable');
    const target = document.querySelectorAll('article')[2];
    target.scrollIntoView = jest.fn();
    target.getBoundingClientRect = () => ({ top: 100, bottom: 150 });

    expect(focusAdjacentFeedItem(container, 0, 1, 62)).toBe(target);
    expect(document.activeElement).toBe(target);
  });

  it('does not skip the first column in single-column mode', () => {
    expect(columnNumberForLayout('1', false)).toBe(1);
    expect(columnNumberForLayout('1', true)).toBe(2);
  });
});
