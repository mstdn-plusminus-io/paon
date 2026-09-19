import {
  apiRequestDelete,
  apiRequestGet,
  apiRequestPost,
  apiRequestPut,
} from 'mastodon/api';

import {
  apiAddCollectionItem,
  apiCreateCollection,
  apiDeleteCollection,
  apiDeleteCollectionItem,
  apiGetAccountInCollections,
  apiRevokeCollectionItem,
  apiUpdateCollection,
} from '../collections';

jest.mock('mastodon/api', () => ({
  apiRequestDelete: jest.fn(),
  apiRequestGet: jest.fn(),
  apiRequestPost: jest.fn(),
  apiRequestPut: jest.fn(),
}));

describe('Mastodon 4.6 collection API client', () => {
  beforeEach(() => {
    jest.clearAllMocks();
  });

  it('uses the stable endpoints for collection membership actions', async () => {
    await apiGetAccountInCollections('10');
    await apiAddCollectionItem('42', '20');
    await apiDeleteCollectionItem('42', '7');
    await apiRevokeCollectionItem('42', '7');

    expect(apiRequestGet).toHaveBeenCalledWith(
      'v1/accounts/10/in_collections',
    );
    expect(apiRequestPost).toHaveBeenNthCalledWith(
      1,
      'v1/collections/42/items',
      { account_id: '20' },
    );
    expect(apiRequestDelete).toHaveBeenCalledWith(
      'v1/collections/42/items/7',
    );
    expect(apiRequestPost).toHaveBeenNthCalledWith(
      2,
      'v1/collections/42/items/7/revoke',
    );
  });

  it('passes every editable attribute and deletes through the stable API', async () => {
    const attributes = {
      name: 'Friends',
      description: 'People I know',
      language: 'en',
      sensitive: true,
      discoverable: false,
      tag_name: 'friends',
    };

    await apiUpdateCollection('42', attributes);
    await apiDeleteCollection('42');

    expect(apiRequestPut).toHaveBeenCalledWith(
      'v1/collections/42',
      attributes,
    );
    expect(apiRequestDelete).toHaveBeenCalledWith('v1/collections/42');
  });

  it('provides both required boolean fields when creating a collection', async () => {
    await apiCreateCollection('Friends');

    expect(apiRequestPost).toHaveBeenCalledWith('v1/collections', {
      name: 'Friends',
      sensitive: false,
      discoverable: true,
    });
  });
});
