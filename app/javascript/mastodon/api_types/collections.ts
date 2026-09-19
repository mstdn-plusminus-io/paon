import type { ApiAccountJSON } from './notifications';

export type ApiCollectionItemState =
  | 'pending'
  | 'accepted'
  | 'rejected'
  | 'revoked';

export interface ApiCollectionItemJSON {
  id: string;
  state: ApiCollectionItemState;
  account_id?: string;
  created_at: string;
}

export interface ApiCollectionJSON {
  id: string;
  uri: string;
  name: string;
  description: string | null;
  language: string | null;
  account_id: string;
  local: boolean;
  sensitive: boolean;
  discoverable: boolean;
  url: string;
  item_count: number;
  created_at: string;
  updated_at: string;
  tag: { name: string; url: string } | null;
  items: ApiCollectionItemJSON[];
}

export interface ApiCollectionsResponseJSON {
  collections: ApiCollectionJSON[];
}

export interface ApiCollectionResponseJSON {
  collection: ApiCollectionJSON;
  accounts: ApiAccountJSON[];
}

export type ApiCollectionUpdateJSON = Partial<
  Pick<
    ApiCollectionJSON,
    'name' | 'description' | 'language' | 'sensitive' | 'discoverable'
  >
> & {
  tag_name?: string;
};

export interface ApiCollectionItemResponseJSON {
  collection_item: ApiCollectionItemJSON;
}
