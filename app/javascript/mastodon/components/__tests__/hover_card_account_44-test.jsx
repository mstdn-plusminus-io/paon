import { IntlProvider } from 'react-intl';

import { MemoryRouter } from 'react-router-dom';

import { fromJS } from 'immutable';
import { Provider } from 'react-redux';

import { render, screen } from '@testing-library/react';

import { HoverCardAccount } from '../hover_card_account';

const createStore = state => ({
  dispatch: jest.fn(),
  getState: () => state,
  subscribe: () => () => undefined,
});

const renderCard = state => render(
  <Provider store={createStore(state)}>
    <IntlProvider locale='en'>
      <MemoryRouter>
        <HoverCardAccount accountId='42' />
      </MemoryRouter>
    </IntlProvider>
  </Provider>,
);

describe('Mastodon 4.4 limited hover cards', () => {
  it('hides profile details and actions for suspended accounts', () => {
    renderCard(fromJS({
      accounts: {
        '42': {
          id: '42',
          acct: 'alice@example.com',
          username: 'alice',
          display_name: 'Alice',
          display_name_html: 'Alice',
          avatar: 'https://example.com/avatar.png',
          avatar_static: 'https://example.com/avatar.png',
          fields: [{ name: 'Secret field', value: 'Secret value' }],
          note_emojified: '<p>Secret biography</p>',
          suspended: true,
        },
      },
      relationships: {
        '42': { note: 'Secret personal note', following: false, requested: false },
      },
    }));

    expect(screen.getByText('Account suspended')).toBeInTheDocument();
    expect(screen.queryByText('Secret biography')).not.toBeInTheDocument();
    expect(screen.queryByText('Secret field')).not.toBeInTheDocument();
    expect(screen.queryByText('Secret personal note')).not.toBeInTheDocument();
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
  });

  it('shows mutual relationship state instead of familiar followers', () => {
    renderCard(fromJS({
      accounts: {
        '42': {
          id: '42', acct: 'alice@example.com', username: 'alice', display_name: 'Alice', display_name_html: 'Alice',
          avatar: 'https://example.com/avatar.png', avatar_static: 'https://example.com/avatar.png', fields: [], note: '', note_emojified: '', followers_count: 1,
        },
        '7': { id: '7', acct: 'bob@example.com', username: 'bob', display_name: 'Bob', display_name_html: 'Bob', avatar: '', avatar_static: '', fields: [] },
      },
      relationships: { '42': { followed_by: true, following: true, requested: false } },
      accounts_familiar_followers: { '42': { loaded: true, accountIds: ['7'] } },
      meta: { me: '1' },
    }));

    expect(screen.getByText('You follow each other')).toBeInTheDocument();
    expect(screen.queryByText(/Followed by Bob/)).not.toBeInTheDocument();
  });

  it('shows familiar followers when the relationship has loaded and is not a follower', () => {
    renderCard(fromJS({
      accounts: {
        '42': {
          id: '42', acct: 'alice@example.com', username: 'alice', display_name: 'Alice', display_name_html: 'Alice',
          avatar: 'https://example.com/avatar.png', avatar_static: 'https://example.com/avatar.png', fields: [], note: '', note_emojified: '', followers_count: 1,
        },
        '7': { id: '7', acct: 'bob@example.com', username: 'bob', display_name: 'Bob', display_name_html: 'Bob', avatar: '', avatar_static: '', fields: [] },
      },
      relationships: { '42': { followed_by: false, following: false, requested: false } },
      accounts_familiar_followers: { '42': { loaded: true, accountIds: ['7'] } },
      meta: { me: '1' },
    }));

    expect(screen.getByText(/Followed by/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Bob' })).toHaveAttribute('href', '/@bob@example.com');
  });
});
