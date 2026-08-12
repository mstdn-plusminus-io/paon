import { shouldFocusSpoilerOnToggle } from '../focus';

describe('shouldFocusSpoilerOnToggle', () => {
  it('does not move focus when adding the first default-sensitive media', () => {
    expect(shouldFocusSpoilerOnToggle(true, true)).toBe(false);
  });

  it('focuses a warning enabled independently of a media upload', () => {
    expect(shouldFocusSpoilerOnToggle(true, false)).toBe(true);
  });
});
