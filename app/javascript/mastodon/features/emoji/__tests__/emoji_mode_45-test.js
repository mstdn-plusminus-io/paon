import {
  EMOJI_MODE_NATIVE,
  EMOJI_MODE_NATIVE_WITH_FLAGS,
  EMOJI_MODE_TWEMOJI,
  isFlagEmoji,
  shouldRenderUnicodeAsImage,
} from '../emoji_mode';

describe('Mastodon 4.5 emoji rendering preference', () => {
  it('keeps ordinary emoji native in native modes', () => {
    expect(shouldRenderUnicodeAsImage('🫩', EMOJI_MODE_NATIVE)).toBe(false);
    expect(shouldRenderUnicodeAsImage('🫩', EMOJI_MODE_NATIVE_WITH_FLAGS)).toBe(false);
  });

  it('uses Twemoji for all unicode emoji in Twemoji mode', () => {
    expect(shouldRenderUnicodeAsImage('🫩', EMOJI_MODE_TWEMOJI)).toBe(true);
  });

  it('replaces flags when the platform lacks flag glyphs', () => {
    expect(isFlagEmoji('🇨🇭')).toBe(true);
    expect(shouldRenderUnicodeAsImage('🇨🇭', EMOJI_MODE_NATIVE_WITH_FLAGS)).toBe(true);
  });
});
