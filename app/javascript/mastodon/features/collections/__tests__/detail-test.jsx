import { IntlProvider } from 'react-intl';

import { fireEvent, render, screen, waitFor } from '@testing-library/react';

import {
  apiAddCollectionItem,
  apiDeleteCollection,
  apiDeleteCollectionItem,
  apiGetCollection,
  apiRevokeCollectionItem,
  apiUpdateCollection,
} from 'mastodon/api/collections';

import { CollectionDetail } from '../detail';

jest.mock('mastodon/api/collections', () => ({
  apiAddCollectionItem: jest.fn(),
  apiDeleteCollection: jest.fn(),
  apiDeleteCollectionItem: jest.fn(),
  apiGetCollection: jest.fn(),
  apiRevokeCollectionItem: jest.fn(),
  apiUpdateCollection: jest.fn(),
}));

jest.mock('mastodon/features/ui/components/column', () => ({
  __esModule: true,
  default: ({ children }) => <div>{children}</div>,
}));

jest.mock('mastodon/initial_state', () => ({
  languages: [
    ['en', 'English', 'English'],
    ['ja', 'Japanese', '日本語'],
  ],
  me: '10',
}));

const collection = (overrides = {}) => ({
  id: '42',
  uri: 'https://example.com/ap/collections/42',
  name: 'Team',
  description: 'Our team',
  language: 'en',
  account_id: '10',
  local: true,
  sensitive: false,
  discoverable: true,
  url: 'https://example.com/collections/42',
  item_count: 1,
  created_at: '2026-01-01T00:00:00.000Z',
  updated_at: '2026-01-01T00:00:00.000Z',
  tag: { name: 'team', url: 'https://example.com/tags/team' },
  items: [
    {
      id: '7',
      state: 'accepted',
      account_id: '20',
      created_at: '2026-01-01T00:00:00.000Z',
    },
  ],
  ...overrides,
});

const accounts = [
  { id: '10', acct: 'owner', display_name: 'Owner' },
  { id: '20', acct: 'member', display_name: 'Member' },
];

const renderDetail = (response) => {
  const history = { push: jest.fn() };
  apiGetCollection.mockResolvedValue(response);
  render(
    <IntlProvider locale='en'>
      <CollectionDetail
        history={history}
        location={{}}
        match={{ params: { id: '42' } }}
      />
    </IntlProvider>,
  );
  return history;
};

describe('<CollectionDetail />', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    apiAddCollectionItem.mockResolvedValue({});
    apiDeleteCollection.mockResolvedValue(undefined);
    apiDeleteCollectionItem.mockResolvedValue(undefined);
    apiRevokeCollectionItem.mockResolvedValue(undefined);
    apiUpdateCollection.mockResolvedValue({});
  });

  it('lets the owner edit metadata, add and remove items, and delete', async () => {
    const history = renderDetail({ collection: collection(), accounts });

    fireEvent.change(await screen.findByLabelText('Name'), {
      target: { value: 'Close friends' },
    });
    fireEvent.change(screen.getByLabelText('Description'), {
      target: { value: 'People I trust' },
    });
    fireEvent.change(screen.getByLabelText('Language'), {
      target: { value: 'ja' },
    });
    fireEvent.change(screen.getByLabelText('Associated hashtag'), {
      target: { value: '#friends' },
    });
    fireEvent.click(screen.getByLabelText('Mark this collection as sensitive'));
    fireEvent.click(
      screen.getByLabelText('Allow others to discover this collection'),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

    await waitFor(() => {
      expect(apiUpdateCollection).toHaveBeenCalledWith('42', {
        name: 'Close friends',
        description: 'People I trust',
        language: 'ja',
        sensitive: true,
        discoverable: false,
        tag_name: '#friends',
      });
    });

    fireEvent.change(screen.getByLabelText('Account ID'), {
      target: { value: '30' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add account' }));
    await waitFor(() => {
      expect(apiAddCollectionItem).toHaveBeenCalledWith('42', '30');
    });

    const removeButton = screen.getByRole('button', { name: 'Remove' });
    await waitFor(() => {
      expect(removeButton).toBeEnabled();
    });
    fireEvent.click(removeButton);
    await waitFor(() => {
      expect(apiDeleteCollectionItem).toHaveBeenCalledWith('42', '7');
    });

    jest.spyOn(window, 'confirm').mockReturnValue(true);
    const deleteButton = screen.getByRole('button', {
      name: 'Delete collection',
    });
    await waitFor(() => {
      expect(deleteButton).toBeEnabled();
    });
    fireEvent.click(deleteButton);
    await waitFor(() => {
      expect(apiDeleteCollection).toHaveBeenCalledWith('42');
      expect(history.push).toHaveBeenCalledWith('/collections');
    });
  });

  it.each(['pending', 'accepted'])(
    'lets the featured account revoke a %s item',
    async (state) => {
      renderDetail({
        collection: collection({
          account_id: '99',
          items: [
            {
              id: '8',
              state,
              account_id: '10',
              created_at: '2026-01-01T00:00:00.000Z',
            },
          ],
        }),
        accounts: [{ id: '10', acct: 'featured', display_name: 'Featured' }],
      });

      fireEvent.click(
        await screen.findByRole('button', { name: 'Remove me' }),
      );

      await waitFor(() => {
        expect(apiRevokeCollectionItem).toHaveBeenCalledWith('42', '8');
      });
      expect(
        screen.queryByRole('button', { name: 'Save changes' }),
      ).not.toBeInTheDocument();
    },
  );
});
