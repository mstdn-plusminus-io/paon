import { shouldRefreshSpellcheckLanguage } from '../autosuggest_textarea';

describe('Mastodon 4.4 composer spellcheck language', () => {
  it('refreshes Firefox spellcheck when the focused textarea language changes', () => {
    const textarea = document.createElement('textarea');
    document.body.appendChild(textarea);
    textarea.focus();

    expect(shouldRefreshSpellcheckLanguage('en', 'ja', textarea)).toBe(true);
    expect(shouldRefreshSpellcheckLanguage('ja', 'ja', textarea)).toBe(false);

    textarea.remove();
  });
});
