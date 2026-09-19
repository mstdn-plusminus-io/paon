import { IntlProvider } from 'react-intl';

import { MemoryRouter } from 'react-router-dom';

import { fireEvent, render, screen, waitFor } from '@testing-library/react';

import {
  apiAddCollectionItem,
  apiGetAccountCollections,
  apiGetAccountInCollections,
} from 'mastodon/api/collections';

import { Collections } from '..';

jest.mock('mastodon/api/collections', () => ({
  apiAddCollectionItem: jest.fn(),
  apiCreateCollection: jest.fn(),
  apiGetAccountCollections: jest.fn(),
  apiGetAccountInCollections: jest.fn(),
}));

jest.mock('mastodon/features/ui/components/column', () => ({
  __esModule: true,
  default: ({ children }) => <div>{children}</div>,
}));

jest.mock('mastodon/initial_state', () => ({ me: '10' }));

const collection = (id, name, accountId) => ({
  id,
  name,
  account_id: accountId,
  item_count: 1,
  items: [],
});

describe('<Collections />', () => {
  beforeEach(() => {
    jest.clearAllMocks();
  });

  it('shows both owned collections and collections featuring the user', async () => {
    apiGetAccountCollections.mockResolvedValue({
      collections: [collection('1', 'Owned collection', '10')],
    });
    apiGetAccountInCollections.mockResolvedValue({
      collections: [
        collection('1', 'Owned collection', '10'),
        collection('2', 'Featured collection', '99'),
      ],
    });

    render(
      <IntlProvider locale='en'>
        <MemoryRouter>
          <Collections />
        </MemoryRouter>
      </IntlProvider>,
    );

    expect(await screen.findByText('Owned collection')).toBeInTheDocument();
    expect(screen.getByText('Featured collection')).toBeInTheDocument();
    expect(screen.getAllByText('Owned collection')).toHaveLength(1);
    expect(apiGetAccountCollections).toHaveBeenCalledWith('10');
    expect(apiGetAccountInCollections).toHaveBeenCalledWith('10');
  });

  it('adds an account selected from the profile menu to an owned collection', async () => {
    apiGetAccountCollections.mockResolvedValue({
      collections: [collection('1', 'Owned collection', '10')],
    });
    apiGetAccountInCollections.mockResolvedValue({ collections: [] });
    apiAddCollectionItem.mockResolvedValue({
      collection_item: { id: '7', state: 'pending' },
    });

    render(
      <IntlProvider locale='en'>
        <MemoryRouter
          initialEntries={['/collections?account_id=99&acct=alice%40remote.test']}
        >
          <Collections />
        </MemoryRouter>
      </IntlProvider>,
    );

    expect(
      await screen.findByText('Choose a collection for @alice@remote.test.'),
    ).toBeInTheDocument();
    expect(await screen.findByText('Owned collection')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Add account' }));
    await waitFor(() => {
      expect(apiAddCollectionItem).toHaveBeenCalledWith('1', '99');
    });
    expect(await screen.findByRole('button', { name: 'Added' })).toBeDisabled();
  });
});
