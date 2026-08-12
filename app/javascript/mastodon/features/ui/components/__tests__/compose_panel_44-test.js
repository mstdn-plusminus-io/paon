import { shouldHideComposePanel } from '../compose_panel_utils';

describe('Mastodon 4.4 compose panel mounting', () => {
  it('hides the side composer when another composer is mounted', () => {
    expect(shouldHideComposePanel(1)).toBe(false);
    expect(shouldHideComposePanel(2)).toBe(true);
  });

  it('tolerates legacy non-numeric mounted state', () => {
    expect(shouldHideComposePanel(true)).toBe(false);
  });
});
