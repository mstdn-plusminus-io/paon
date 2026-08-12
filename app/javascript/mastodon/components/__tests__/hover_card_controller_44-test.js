import {
  ACTIVE_MOUSE_MOVEMENT_THRESHOLD,
  shouldScheduleHoverCardFromPointer,
} from '../hover_card_controller';

describe('hover card pointer intent', () => {
  it('uses Mastodon 4.4 active-movement window', () => {
    expect(ACTIVE_MOUSE_MOVEMENT_THRESHOLD).toBe(150);
  });

  it.each([
    ['stationary pointer', false, false, false],
    ['active touch', true, true, false],
    ['hover card contents', false, true, true],
    ['recent mouse movement', false, true, false],
  ])('%s', (_name, usingTouch, activeMouseMovement, insideHoverCard) => {
    expect(shouldScheduleHoverCardFromPointer({ usingTouch, activeMouseMovement, insideHoverCard }))
      .toBe(!usingTouch && activeMouseMovement && !insideHoverCard);
  });
});
