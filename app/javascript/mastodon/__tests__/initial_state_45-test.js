describe('Mastodon 4.5 legacy browser initial state', () => {
  it('boots when Intl.DisplayNames is unavailable', () => {
    const descriptor = Object.getOwnPropertyDescriptor(Intl, 'DisplayNames');
    Object.defineProperty(Intl, 'DisplayNames', {
      configurable: true,
      value: undefined,
    });
    document.head.innerHTML = '<meta name="initialPath" content="/home">';
    document.body.innerHTML = '<script id="initial-state" type="application/json">{"languages":[["en","English","English"]],"meta":{"emoji_style":"auto"}}</script>';

    try {
      jest.isolateModules(() => {
        const state = jest.requireActual('../initial_state');
        expect(state.languages).toEqual([['en', 'English', 'English']]);
        expect(state.hasMultiColumnPath).toBe(true);
      });
    } finally {
      if (descriptor) {
        Object.defineProperty(Intl, 'DisplayNames', descriptor);
      } else {
        delete Intl.DisplayNames;
      }
      document.head.innerHTML = '';
      document.body.innerHTML = '';
    }
  });
});
