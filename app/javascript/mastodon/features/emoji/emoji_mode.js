export const EMOJI_MODE_NATIVE = 'native';
export const EMOJI_MODE_NATIVE_WITH_FLAGS = 'native-with-flags';
export const EMOJI_MODE_TWEMOJI = 'twemoji';

const FONT_FAMILY = '"Twemoji Mozilla","Apple Color Emoji","Segoe UI Emoji","Noto Color Emoji",sans-serif';

const getFeature = (text, color) => {
  const canvas = document.createElement('canvas');
  canvas.width = canvas.height = 1;
  const context = canvas.getContext('2d', { willReadFrequently: true });
  if (!context) throw new Error('Canvas context unavailable');
  context.textBaseline = 'top';
  context.font = `100px ${FONT_FAMILY}`;
  context.fillStyle = color;
  context.scale(0.01, 0.01);
  context.fillText(text, 0, 0);
  return [...context.getImageData(0, 0, 1, 1).data].join(',');
};

const supportsColorEmoji = emoji => {
  const black = getFeature(emoji, '#000');
  const white = getFeature(emoji, '#fff');
  return black === white && !black.startsWith('0,0,0,');
};

const needsTwemoji = () => {
  try {
    return !supportsColorEmoji('🫩');
  } catch {
    return true;
  }
};

const needsFlagFallback = () => {
  try {
    return !supportsColorEmoji('🇨🇭');
  } catch {
    return true;
  }
};

export const determineEmojiMode = style => {
  if (style === EMOJI_MODE_TWEMOJI) return EMOJI_MODE_TWEMOJI;
  if (style === EMOJI_MODE_NATIVE) {
    return needsFlagFallback() ? EMOJI_MODE_NATIVE_WITH_FLAGS : EMOJI_MODE_NATIVE;
  }
  if (needsTwemoji()) return EMOJI_MODE_TWEMOJI;
  return needsFlagFallback() ? EMOJI_MODE_NATIVE_WITH_FLAGS : EMOJI_MODE_NATIVE;
};

export const isFlagEmoji = emoji => /^(?:\p{Regional_Indicator}{2}|\p{Emoji}.*\u200d?\u{1f3f4})$/u.test(emoji);

export const shouldRenderUnicodeAsImage = (emoji, mode) => (
  mode === EMOJI_MODE_TWEMOJI ||
  (mode === EMOJI_MODE_NATIVE_WITH_FLAGS && isFlagEmoji(emoji))
);
