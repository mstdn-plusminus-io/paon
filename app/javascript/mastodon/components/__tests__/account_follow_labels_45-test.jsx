import { createIntl, IntlProvider } from 'react-intl';

import { MemoryRouter } from 'react-router-dom';

import { fromJS } from 'immutable';
import { Provider } from 'react-redux';

import { render, screen } from '@testing-library/react';

import { Account } from '../account';

const intl = createIntl({ locale: 'en' });
const noop = () => undefined;
const store = {
  dispatch: jest.fn(),
  getState: () => fromJS({ dropdown_menu: {} }),
  subscribe: () => () => undefined,
};

const accountWith = (overrides = {}) => fromJS({
  id: '42',
  acct: 'alice@example.com',
  username: 'alice',
  display_name: 'Alice',
  display_name_html: 'Alice',
  avatar: 'https://example.com/avatar.png',
  avatar_static: 'https://example.com/avatar.png',
  fields: [],
  followers_count: 1,
  relationship: {
    blocking: false,
    muting: false,
    requested: false,
    following: false,
    followed_by: false,
  },
  ...overrides,
});

const renderAccount = (account) => render(
  <Provider store={store}>
    <IntlProvider locale='en'>
      <MemoryRouter>
        <Account
          account={account}
          intl={intl}
          onFollow={noop}
          onBlock={noop}
          onMute={noop}
          onMuteNotifications={noop}
        />
      </MemoryRouter>
    </IntlProvider>
  </Provider>,
);

describe('Mastodon 4.5 follow action labels', () => {
  it('labels locked accounts as a follow request', () => {
    renderAccount(accountWith({ locked: true }));

    expect(screen.getByRole('button', { name: 'Request to follow' })).toBeInTheDocument();
  });

  it('labels an outgoing request as cancel request', () => {
    renderAccount(accountWith({ relationship: { requested: true } }));

    expect(screen.getByRole('button', { name: 'Cancel request' })).toBeInTheDocument();
  });

  it('labels a reciprocal follow action as follow back', () => {
    renderAccount(accountWith({ relationship: { followed_by: true } }));

    expect(screen.getByRole('button', { name: 'Follow back' })).toBeInTheDocument();
  });
});
