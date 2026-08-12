import { IntlProvider } from 'react-intl';

import { MemoryRouter } from 'react-router-dom';

import { fromJS, List as ImmutableList } from 'immutable';

import { render, screen } from '@testing-library/react';

import { FamiliarFollowersView } from '../familiar_followers';

const account = (id, name) => fromJS({
  id,
  username: name.toLowerCase(),
  acct: `${name.toLowerCase()}@example.com`,
  display_name: name,
  display_name_html: name,
  avatar: `https://example.com/${id}.png`,
  avatar_static: `https://example.com/${id}.png`,
});

const renderView = (props) => render(
  <IntlProvider locale='en'>
    <MemoryRouter>
      <FamiliarFollowersView {...props} />
    </MemoryRouter>
  </IntlProvider>,
);

describe('Mastodon 4.4 familiar followers profile UI', () => {
  it('renders nothing while loading or when the API result is empty', () => {
    const loading = renderView({ familiarFollowers: null, isLoading: true });
    expect(loading.container).toBeEmptyDOMElement();
    loading.unmount();

    const empty = renderView({ familiarFollowers: ImmutableList(), isLoading: false });
    expect(empty.container).toBeEmptyDOMElement();
  });

  it('limits avatars to three and keeps profile links keyboard-focusable', () => {
    const familiarFollowers = ImmutableList([
      account('1', 'Alice'),
      account('2', 'Bob'),
      account('3', 'Carol'),
      account('4', 'Dave'),
    ]);

    renderView({ familiarFollowers, isLoading: false });

    const avatarLinks = screen.getAllByRole('link', { name: /@.*example\.com/ });
    expect(avatarLinks).toHaveLength(3);
    expect(screen.getByText(/2 others you know/)).toBeInTheDocument();

    avatarLinks[0].focus();
    expect(avatarLinks[0]).toHaveFocus();
    expect(avatarLinks[0]).toHaveAttribute('href', '/@alice@example.com');
  });
});
