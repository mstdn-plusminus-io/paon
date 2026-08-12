import { navigateBack } from '../back_navigation';

describe('Mastodon 4.4 Backspace navigation', () => {
  it('prevents browser navigation before returning through Mastodon history', () => {
    const event = { preventDefault: jest.fn() };
    const history = {
      location: { state: { fromMastodon: true } },
      goBack: jest.fn(),
      push: jest.fn(),
    };

    navigateBack(event, history);

    expect(event.preventDefault).toHaveBeenCalledTimes(1);
    expect(history.goBack).toHaveBeenCalledTimes(1);
    expect(history.push).not.toHaveBeenCalled();
  });

  it('prevents browser navigation before falling back to the home route', () => {
    const event = { preventDefault: jest.fn() };
    const history = {
      location: {},
      goBack: jest.fn(),
      push: jest.fn(),
    };

    navigateBack(event, history);

    expect(event.preventDefault).toHaveBeenCalledTimes(1);
    expect(history.goBack).not.toHaveBeenCalled();
    expect(history.push).toHaveBeenCalledWith('/');
  });
});
