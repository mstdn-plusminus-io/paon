import { displayLanguageName, guessLanguage } from '../language_detection';

const mockLande = jest.fn();

jest.mock('lande', () => ({
  __esModule: true,
  default: (...args) => mockLande(...args),
}));

jest.mock('../url_regex', () => ({
  urlRegex: /https?:\/\/\S+/gi,
}));

describe('compose language detection', () => {
  it('detects high-confidence Japanese and English text', () => {
    mockLande
      .mockReturnValueOnce([['jpn', 0.99]])
      .mockReturnValueOnce([['eng', 0.99]]);

    expect(guessLanguage('これは日本語で書かれた十分に長い文章です。言語を正しく判定します。')).toBe('ja');
    expect(guessLanguage('This is a sufficiently long English sentence for accurate language detection.')).toBe('en');
  });

  it('does not guess from short text, URLs, or remote mentions', () => {
    mockLande.mockClear();

    expect(guessLanguage('short post')).toBe('');
    expect(guessLanguage('https://example.com/a/very/long/path @alice@example.com')).toBe('');
    expect(mockLande).not.toHaveBeenCalled();
  });

  it('uses the interface locale for language names', () => {
    expect(displayLanguageName('ja', 'en', 'English')).toBe('英語');
  });
});
