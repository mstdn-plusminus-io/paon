import { useCallback, useEffect, useState } from 'react';

import { FormattedMessage, useIntl } from 'react-intl';

import { Link, useLocation } from 'react-router-dom';

import {
  apiCreateCollection,
  apiAddCollectionItem,
  apiGetAccountCollections,
  apiGetAccountInCollections,
} from 'mastodon/api/collections';
import type { ApiCollectionJSON } from 'mastodon/api_types/collections';
import Button from 'mastodon/components/button';
import Column from 'mastodon/features/ui/components/column';
import { me } from 'mastodon/initial_state';

export const Collections: React.FC = () => {
  const intl = useIntl();
  const location = useLocation();
  const query = new URLSearchParams(location.search);
  const requestedAccountId = query.get('account_id') ?? '';
  const targetAccountId = /^\d+$/.test(requestedAccountId)
    ? requestedAccountId
    : '';
  const targetAcct = query.get('acct') ?? targetAccountId;
  const [collections, setCollections] = useState<ApiCollectionJSON[]>([]);
  const [featuredCollections, setFeaturedCollections] = useState<
    ApiCollectionJSON[]
  >([]);
  const [name, setName] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [failed, setFailed] = useState(false);
  const [actionFailed, setActionFailed] = useState(false);
  const [addingTo, setAddingTo] = useState<string>();
  const [addedTo, setAddedTo] = useState<Set<string>>(new Set());

  const load = useCallback(async () => {
    if (!me) {
      setLoading(false);
      return;
    }
    setLoading(true);
    setFailed(false);
    try {
      const [ownedResponse, featuredResponse] = await Promise.all([
        apiGetAccountCollections(me),
        apiGetAccountInCollections(me),
      ]);
      setCollections(ownedResponse.collections);
      const ownedIds = new Set(
        ownedResponse.collections.map((collection) => collection.id),
      );
      setFeaturedCollections(
        featuredResponse.collections.filter(
          (collection) => !ownedIds.has(collection.id),
        ),
      );
    } catch {
      setFailed(true);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const create = useCallback(async () => {
    const value = name.trim();
    if (!value || saving) return;
    setSaving(true);
    setActionFailed(false);
    try {
      const response = await apiCreateCollection(value);
      setCollections((current) => [response.collection, ...current]);
      setName('');
    } catch {
      setActionFailed(true);
    } finally {
      setSaving(false);
    }
  }, [name, saving]);
  const handleNameChange = useCallback<
    React.ChangeEventHandler<HTMLInputElement>
  >((event) => {
    setName(event.target.value);
  }, []);
  const addTargetToCollection = useCallback(
    async (collectionId: string) => {
      if (!targetAccountId || addingTo) return;
      setAddingTo(collectionId);
      setActionFailed(false);
      try {
        await apiAddCollectionItem(collectionId, targetAccountId);
        setAddedTo((current) => new Set(current).add(collectionId));
        setCollections((current) =>
          current.map((collection) =>
            collection.id === collectionId
              ? { ...collection, item_count: collection.item_count + 1 }
              : collection,
          ),
        );
      } catch {
        setActionFailed(true);
      } finally {
        setAddingTo(undefined);
      }
    },
    [addingTo, targetAccountId],
  );
  const handleAddTargetToCollection = useCallback<
    React.MouseEventHandler<HTMLButtonElement>
  >(
    (event) => {
      const collectionId = event.currentTarget.dataset.collectionId;
      if (collectionId) void addTargetToCollection(collectionId);
    },
    [addTargetToCollection],
  );

  return (
    <Column
      heading={intl.formatMessage({
        id: 'collections.title',
        defaultMessage: 'Collections',
      })}
    >
      <div className='paon-feature-page'>
        <h1>
          <FormattedMessage
            id='collections.title'
            defaultMessage='Collections'
          />
        </h1>
        <p>
          <FormattedMessage
            id='collections.subtitle'
            defaultMessage='Create and share groups of accounts.'
          />
        </p>
        {targetAccountId && (
          <p role='status'>
            <FormattedMessage
              id='collections.choose_for_account'
              defaultMessage='Choose a collection for @{acct}.'
              values={{ acct: targetAcct }}
            />
          </p>
        )}
        <div className='paon-feature-page__form'>
          <label>
            <FormattedMessage id='collections.name' defaultMessage='Name' />
            <input
              type='text'
              value={name}
              maxLength={40}
              onChange={handleNameChange}
            />
          </label>
          <Button
            disabled={!name.trim() || saving}
            loading={saving}
            text={intl.formatMessage({
              id: 'collections.create',
              defaultMessage: 'Create collection',
            })}
            onClick={create}
          />
        </div>
        {actionFailed && (
          <p role='alert'>
            <FormattedMessage
              id='collections.action_error'
              defaultMessage='The collection could not be changed. Please try again.'
            />
          </p>
        )}
        {loading && (
          <p>
            <FormattedMessage
              id='loading_indicator.label'
              defaultMessage='Loading…'
            />
          </p>
        )}
        {failed && (
          <p role='alert'>
            <FormattedMessage
              id='collections.error'
              defaultMessage='Collections could not be loaded.'
            />
          </p>
        )}
        {!loading && !failed && (
          <>
            <section className='paon-feature-page__section'>
              <h2>
                <FormattedMessage
                  id='collections.yours'
                  defaultMessage='Your collections'
                />
              </h2>
              {collections.length === 0 ? (
                <p>
                  <FormattedMessage
                    id='collections.empty'
                    defaultMessage='No collections yet.'
                  />
                </p>
              ) : (
                <ul
                  className={`paon-feature-page__list${
                    targetAccountId ? ' paon-feature-page__list--accounts' : ''
                  }`}
                >
                  {collections.map((collection) => (
                    <li key={collection.id}>
                      <div>
                        <Link to={`/collections/${collection.id}`}>
                          {collection.name}
                        </Link>
                        <span>
                          <FormattedMessage
                            id='collections.item_count'
                            defaultMessage='{count, plural, one {# account} other {# accounts}}'
                            values={{ count: collection.item_count }}
                          />
                        </span>
                      </div>
                      {targetAccountId && (
                        <button
                          type='button'
                          className='button'
                          data-collection-id={collection.id}
                          disabled={
                            Boolean(addingTo) || addedTo.has(collection.id)
                          }
                          onClick={handleAddTargetToCollection}
                        >
                          {addingTo === collection.id ? (
                            <FormattedMessage
                              id='loading_indicator.label'
                              defaultMessage='Loading…'
                            />
                          ) : addedTo.has(collection.id) ? (
                            <FormattedMessage
                              id='collections.added_account'
                              defaultMessage='Added'
                            />
                          ) : (
                            <FormattedMessage
                              id='collections.add_account'
                              defaultMessage='Add account'
                            />
                          )}
                        </button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </section>
            <section className='paon-feature-page__section'>
              <h2>
                <FormattedMessage
                  id='collections.featuring_you'
                  defaultMessage='Collections featuring you'
                />
              </h2>
              {featuredCollections.length === 0 ? (
                <p>
                  <FormattedMessage
                    id='collections.not_featured'
                    defaultMessage='You are not featured in any collections.'
                  />
                </p>
              ) : (
                <ul className='paon-feature-page__list'>
                  {featuredCollections.map((collection) => (
                    <li key={collection.id}>
                      <Link to={`/collections/${collection.id}`}>
                        {collection.name}
                      </Link>
                      <span>
                        <FormattedMessage
                          id='collections.item_count'
                          defaultMessage='{count, plural, one {# account} other {# accounts}}'
                          values={{ count: collection.item_count }}
                        />
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </>
        )}
      </div>
    </Column>
  );
};

export default Collections;
