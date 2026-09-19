import {
  apiRequestDelete,
  apiRequestGet,
  apiRequestPost,
  apiRequestPut,
} from '../api';
import type {
  ApiCollectionJSON,
  ApiCollectionItemResponseJSON,
  ApiCollectionResponseJSON,
  ApiCollectionsResponseJSON,
  ApiCollectionUpdateJSON,
} from '../api_types/collections';

export const apiGetAccountCollections = (accountId: string) =>
  apiRequestGet<ApiCollectionsResponseJSON>(
    `v1/accounts/${accountId}/collections`,
  );

export const apiGetAccountInCollections = (accountId: string) =>
  apiRequestGet<ApiCollectionsResponseJSON>(
    `v1/accounts/${accountId}/in_collections`,
  );

export const apiGetCollection = (id: string) =>
  apiRequestGet<ApiCollectionResponseJSON>(`v1/collections/${id}`);

export const apiCreateCollection = (name: string) =>
  apiRequestPost<{ collection: ApiCollectionJSON }>('v1/collections', {
    name,
    sensitive: false,
    discoverable: true,
  });

export const apiUpdateCollection = (
  id: string,
  attributes: ApiCollectionUpdateJSON,
) =>
  apiRequestPut<{ collection: ApiCollectionJSON }>(
    `v1/collections/${id}`,
    attributes,
  );

export const apiDeleteCollection = (id: string) =>
  apiRequestDelete(`v1/collections/${id}`);

export const apiAddCollectionItem = (collectionId: string, accountId: string) =>
  apiRequestPost<ApiCollectionItemResponseJSON>(
    `v1/collections/${collectionId}/items`,
    { account_id: accountId },
  );

export const apiDeleteCollectionItem = (collectionId: string, itemId: string) =>
  apiRequestDelete(`v1/collections/${collectionId}/items/${itemId}`);

export const apiRevokeCollectionItem = (collectionId: string, itemId: string) =>
  apiRequestPost(`v1/collections/${collectionId}/items/${itemId}/revoke`);
