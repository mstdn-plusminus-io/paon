import { statusClickDisposition } from '../../utils/status_navigation';

describe('Mastodon 4.4 status navigation', () => {
  it('opens regular clicks in place', () => {
    expect(statusClickDisposition({ button: 0 })).toBe('current');
    expect(statusClickDisposition()).toBe('current');
  });

  it('opens middle and modified primary clicks in a new tab', () => {
    expect(statusClickDisposition({ button: 1 })).toBe('new');
    expect(statusClickDisposition({ button: 0, ctrlKey: true })).toBe('new');
    expect(statusClickDisposition({ button: 0, metaKey: true })).toBe('new');
  });

  it('ignores unrelated mouse buttons', () => {
    expect(statusClickDisposition({ button: 2 })).toBeNull();
  });
});
