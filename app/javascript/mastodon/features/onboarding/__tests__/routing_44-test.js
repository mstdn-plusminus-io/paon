import { onboardingStepFromPath } from '../index';

describe('Mastodon 4.4 onboarding routes', () => {
  it('opens both /start and /start/profile at profile setup', () => {
    expect(onboardingStepFromPath('/start')).toBe('profile');
    expect(onboardingStepFromPath('/start/profile')).toBe('profile');
  });

  it('opens /start/follows at follow suggestions', () => {
    expect(onboardingStepFromPath('/start/follows')).toBe('follows');
  });

  it('does not restore removed summary and sharing steps', () => {
    expect(onboardingStepFromPath('/start/share')).toBe('profile');
    expect(onboardingStepFromPath('/start/summary')).toBe('profile');
  });
});
