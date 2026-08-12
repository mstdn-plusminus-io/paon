import { createIntl, IntlProvider } from 'react-intl';

import { MemoryRouter } from 'react-router-dom';

import { fromJS } from 'immutable';

import { render, screen } from '@testing-library/react';

import { Account } from '../account';

const intl = createIntl({ locale: 'en' });
const noop = () => undefined;

describe('Mastodon 4.4 follow suggestion hover cards', () => {
  it('marks the shared account link used by follow suggestions as hoverable', () => {
    const account = fromJS({
      id: '42',
      acct: 'alice@example.com',
      username: 'alice',
      display_name: 'Alice',
      display_name_html: 'Alice',
      avatar: 'https://example.com/avatar.png',
      avatar_static: 'https://example.com/avatar.png',
      fields: [],
      followers_count: 1,
      relationship: null,
    });

    render(
      <IntlProvider locale='en'>
        <MemoryRouter>
          <Account
            account={account}
            intl={intl}
            minimal
            onFollow={noop}
            onBlock={noop}
            onMute={noop}
            onMuteNotifications={noop}
          />
        </MemoryRouter>
      </IntlProvider>,
    );

    expect(screen.getByRole('link')).toHaveAttribute('data-hover-card-account', '42');
  });
});
