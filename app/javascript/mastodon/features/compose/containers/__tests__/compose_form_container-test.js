import { fromJS } from 'immutable';

import { getComposeSuccessRedirect } from '../../util/compose_redirect';
import { findMissingAltTextMediaId } from '../../util/missing_alt_text';

describe('getComposeSuccessRedirect', () => {
  const status = { url: 'https://example.com/@alice/10' };

  it('returns the created status URL for standalone compose', () => {
    expect(getComposeSuccessRedirect(true, status)).toBe(status.url);
  });

  it('does not redirect the regular compose flow', () => {
    expect(getComposeSuccessRedirect(false, status)).toBeNull();
  });
});

describe('findMissingAltTextMediaId', () => {
  it('returns the first image or GIFV without a description', () => {
    const media = fromJS([
      { id: 'video', type: 'video', description: '' },
      { id: 'described', type: 'image', description: 'A cat' },
      { id: 'missing', type: 'gifv', description: null },
      { id: 'later', type: 'image', description: '' },
    ]);

    expect(findMissingAltTextMediaId(media)).toBe('missing');
  });

  it('returns undefined when all applicable media have descriptions', () => {
    const media = fromJS([
      { id: 'image', type: 'image', description: 'A cat' },
      { id: 'audio', type: 'audio', description: '' },
    ]);

    expect(findMissingAltTextMediaId(media)).toBeUndefined();
  });
});
