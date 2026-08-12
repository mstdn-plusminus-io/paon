import { shouldDismissMediaSwipe } from '../media_swipe';

describe('shouldDismissMediaSwipe', () => {
  it('accepts a deliberate vertical swipe in either direction', () => {
    expect(shouldDismissMediaSwipe({ x: 10, y: 100 }, { x: 15, y: 0 })).toBe(true);
    expect(shouldDismissMediaSwipe({ x: 10, y: 0 }, { x: 15, y: 100 })).toBe(true);
  });

  it('ignores horizontal gestures, short gestures, and gestures while zoomed', () => {
    expect(shouldDismissMediaSwipe({ x: 0, y: 0 }, { x: 100, y: 20 })).toBe(false);
    expect(shouldDismissMediaSwipe({ x: 0, y: 0 }, { x: 0, y: 60 })).toBe(false);
    expect(shouldDismissMediaSwipe({ x: 0, y: 0 }, { x: 0, y: 100 }, true)).toBe(false);
  });
});
