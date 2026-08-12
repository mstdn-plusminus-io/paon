import mockPropTypes from 'prop-types';

import { IntlProvider } from 'react-intl';

import { Router } from 'react-router-dom';

import { fromJS } from 'immutable';
import { Provider } from 'react-redux';

import { fireEvent, render, screen } from '@testing-library/react';
import { createMemoryHistory } from 'history';

import { HashtagBar } from 'mastodon/components/hashtag_bar';

import { HashtagMenuController } from '../hashtag_menu_controller';

jest.mock('react-overlays/Overlay', () => {
  const MockOverlay = ({ children }) => children({
    props: {},
    arrowProps: {},
    placement: 'bottom',
  });

  MockOverlay.propTypes = {
    children: mockPropTypes.func.isRequired,
  };

  return MockOverlay;
});

const createStore = state => ({
  dispatch: jest.fn(),
  getState: () => state,
  subscribe: () => () => undefined,
});

describe('Mastodon 4.4 hashtag quick menu', () => {
  it('opens from a hashtag bar and navigates to the account-filtered tag', () => {
    const history = createMemoryHistory();
    const store = createStore(fromJS({
      accounts: {
        '42': { id: '42', username: 'alice', acct: 'alice@example.com' },
      },
    }));

    render(
      <Provider store={store}>
        <IntlProvider locale='en'>
          <Router history={history}>
            <HashtagBar hashtags={['paon']} accountId='42' />
            <HashtagMenuController />
          </Router>
        </IntlProvider>
      </Provider>,
    );

    const hashtag = screen.getByRole('link', { name: '# paon' });
    expect(hashtag).toHaveAttribute('data-menu-hashtag', '42');

    fireEvent.click(hashtag);
    expect(screen.getByRole('button', { name: 'Mute #paon' })).toHaveAttribute('href', '/filters');
    fireEvent.click(screen.getByRole('button', {
      name: 'Browse posts from @alice in #paon',
    }));

    expect(history.location.pathname).toBe('/@alice@example.com/tagged/paon');
  });
});
