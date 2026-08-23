import { textAtCursorMatchesToken } from '../autosuggest_input';

const searchTokens = ['@', '＠', ':', '#', '＃'];

describe('Mastodon 4.5 autosuggest input tokens', () => {
  it.each([
    ['＃Paon', '＃Paon'],
    ['＠Alice', '＠Alice'],
  ])('recognizes the full-width token in %s', (text, token) => {
    expect(textAtCursorMatchesToken(text, text.length, searchTokens)).toEqual([1, token]);
  });
});
