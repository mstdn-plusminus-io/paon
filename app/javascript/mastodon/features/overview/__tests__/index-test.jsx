import { IntlProvider } from 'react-intl';

import { fromJS } from 'immutable';

import { fireEvent, render, screen } from '@testing-library/react';

import { fetchExtendedDescription, fetchServer } from 'mastodon/actions/server';
import { apiRequestGet } from 'mastodon/api';
import { useAppDispatch, useAppSelector } from 'mastodon/store';

import { Overview } from '..';

jest.mock('mastodon/actions/importer', () => ({
  importFetchedStatuses: jest.fn(() => ({ type: 'STATUSES_IMPORT' })),
}));

jest.mock('mastodon/actions/server', () => ({
  fetchExtendedDescription: jest.fn(() => ({
    type: 'EXTENDED_DESCRIPTION_REQUEST',
  })),
  fetchServer: jest.fn(() => ({ type: 'SERVER_FETCH_REQUEST' })),
}));

jest.mock('mastodon/api', () => ({
  apiRequestGet: jest.fn(),
}));

jest.mock('mastodon/store', () => ({
  useAppDispatch: jest.fn(),
  useAppSelector: jest.fn(),
}));

jest.mock('mastodon/initial_state', () => ({ domain: 'fallback.example' }));

jest.mock('mastodon/components/server_hero_image', () => ({
  // eslint-disable-next-line react/prop-types -- Minimal component test double.
  ServerHeroImage: ({ src }) => <img data-testid='server-hero' src={src} alt='' />,
}));

jest.mock('mastodon/containers/status_container', () => ({
  __esModule: true,
  default: ({ id }) => <div>Status {id}</div>,
}));

jest.mock('mastodon/features/ui/components/column', () => ({
  __esModule: true,
  default: ({ children }) => <div>{children}</div>,
}));

describe('<Overview />', () => {
  const dispatch = jest.fn();
  const state = fromJS({
    server: {
      server: {
        isLoading: false,
        domain: 'social.example',
        title: 'Example Community',
        description: 'A friendly place for local conversations.',
        thumbnail: {
          url: 'https://social.example/hero.jpg',
          blurhash: 'LEHV6nWB2yk8pyo0adR*.7kCMdnj',
        },
        contact: {
          email: 'admin@social.example',
          account: {
            acct: 'admin',
            display_name: 'Admin Alice',
            url: 'https://social.example/@admin',
          },
        },
      },
      extendedDescription: {
        isLoading: false,
        content: '<p>Extended community details.</p>',
      },
    },
  });

  beforeEach(() => {
    jest.clearAllMocks();
    useAppDispatch.mockReturnValue(dispatch);
    useAppSelector.mockImplementation((selector) => selector(state));
    apiRequestGet.mockResolvedValue([{ id: '101' }, { id: '102' }]);
  });

  it('loads the server and latest 40 local public posts', async () => {
    render(
      <IntlProvider locale='en'>
        <Overview />
      </IntlProvider>,
    );

    expect(screen.getByText('Example Community')).toBeInTheDocument();
    expect(
      screen.getByText('A friendly place for local conversations.'),
    ).toBeInTheDocument();
    expect(screen.getByTestId('server-hero')).toHaveAttribute(
      'src',
      'https://social.example/hero.jpg',
    );
    expect(await screen.findByText('Status 101')).toBeInTheDocument();
    expect(screen.getByText('Status 102')).toBeInTheDocument();
    expect(apiRequestGet).toHaveBeenCalledWith('v1/timelines/public', {
      local: true,
      limit: 40,
    });
    expect(fetchServer).toHaveBeenCalledTimes(1);
    expect(fetchExtendedDescription).toHaveBeenCalledTimes(1);
  });

  it('shows the administrator, contact, and extended description in About', async () => {
    render(
      <IntlProvider locale='en'>
        <Overview />
      </IntlProvider>,
    );

    expect(await screen.findByText('Status 101')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: 'About this server' }));

    expect(screen.getByText('Admin Alice')).toBeInTheDocument();
    expect(screen.getByText('@admin')).toBeInTheDocument();
    expect(screen.getByText('admin@social.example')).toHaveAttribute(
      'href',
      'mailto:admin@social.example',
    );
    expect(screen.getByText('Extended community details.')).toBeInTheDocument();
    expect(screen.queryByText('Status 101')).not.toBeInTheDocument();
  });
});
