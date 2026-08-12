import { decodeLinkTimelineURL } from '../url';

describe('decodeLinkTimelineURL', () => {
  it('decodes the URL used by the /links/:url route', () => {
    const url = 'https://example.com/story?a=one two&b=%already-encoded';

    expect(decodeLinkTimelineURL(encodeURIComponent(url))).toEqual(url);
  });

  it.each([
    undefined,
    '%E0%A4%A',
    encodeURIComponent('javascript:alert(1)'),
    encodeURIComponent('https://user:secret@example.com/story'),
    encodeURIComponent(' https://example.com/story'),
  ])('rejects an unsafe or malformed route parameter: %s', value => {
    expect(decodeLinkTimelineURL(value)).toBeNull();
  });
});
