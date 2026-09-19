import { useCallback, useEffect, useMemo, useState } from 'react';
import type { ChangeEventHandler, FormEventHandler } from 'react';

import { FormattedMessage, useIntl } from 'react-intl';

import type { RouteComponentProps } from 'react-router-dom';

import {
  apiAddCollectionItem,
  apiDeleteCollection,
  apiDeleteCollectionItem,
  apiGetCollection,
  apiRevokeCollectionItem,
  apiUpdateCollection,
} from 'mastodon/api/collections';
import type {
  ApiCollectionItemJSON,
  ApiCollectionResponseJSON,
  ApiCollectionUpdateJSON,
} from 'mastodon/api_types/collections';
import type { ApiAccountJSON } from 'mastodon/api_types/notifications';
import Button from 'mastodon/components/button';
import Column from 'mastodon/features/ui/components/column';
import { languages, me } from 'mastodon/initial_state';

type Props = RouteComponentProps<{ id: string }>;

interface CollectionDraft {
  name: string;
  description: string;
  language: string;
  sensitive: boolean;
  discoverable: boolean;
  tagName: string;
}

const CollectionItemState: React.FC<{
  state: ApiCollectionItemJSON['state'];
}> = ({ state }) => {
  switch (state) {
    case 'accepted':
      return (
        <FormattedMessage
          id='collections.item_state.accepted'
          defaultMessage='Accepted'
        />
      );
    case 'rejected':
      return (
        <FormattedMessage
          id='collections.item_state.rejected'
          defaultMessage='Rejected'
        />
      );
    case 'revoked':
      return (
        <FormattedMessage
          id='collections.item_state.revoked'
          defaultMessage='Revoked'
        />
      );
    case 'pending':
    default:
      return (
        <FormattedMessage
          id='collections.item_state.pending'
          defaultMessage='Pending'
        />
      );
  }
};

interface CollectionItemRowProps {
  item: ApiCollectionItemJSON;
  account?: ApiAccountJSON;
  isOwner: boolean;
  canRevoke: boolean;
  busyAction?: string;
  onRemove: (itemId: string) => void;
  onRevoke: (itemId: string) => void;
}

const CollectionItemRow: React.FC<CollectionItemRowProps> = ({
  item,
  account,
  isOwner,
  canRevoke,
  busyAction,
  onRemove,
  onRevoke,
}) => {
  const handleRemove = useCallback(() => {
    onRemove(item.id);
  }, [item.id, onRemove]);
  const handleRevoke = useCallback(() => {
    onRevoke(item.id);
  }, [item.id, onRevoke]);

  return (
    <li>
      <div>
        {account ? (
          <a href={`/@${account.acct}`}>
            {account.display_name || `@${account.acct}`}
          </a>
        ) : (
          <span>
            <FormattedMessage
              id='collections.account_unavailable'
              defaultMessage='Unavailable account'
            />
          </span>
        )}
        <small>
          <CollectionItemState state={item.state} />
        </small>
      </div>
      {isOwner && (
        <Button
          secondary
          disabled={Boolean(busyAction)}
          loading={busyAction === `remove-${item.id}`}
          onClick={handleRemove}
        >
          <FormattedMessage
            id='collections.remove_account'
            defaultMessage='Remove'
          />
        </Button>
      )}
      {canRevoke && (
        <Button
          dangerous
          disabled={Boolean(busyAction)}
          loading={busyAction === `revoke-${item.id}`}
          onClick={handleRevoke}
        >
          <FormattedMessage
            id='collections.revoke_account'
            defaultMessage='Remove me'
          />
        </Button>
      )}
    </li>
  );
};

const draftFromResponse = (
  response: ApiCollectionResponseJSON,
): CollectionDraft => ({
  name: response.collection.name,
  description: response.collection.description ?? '',
  language: response.collection.language ?? '',
  sensitive: response.collection.sensitive,
  discoverable: response.collection.discoverable,
  tagName: response.collection.tag?.name ?? '',
});

export const CollectionDetail: React.FC<Props> = ({ history, match }) => {
  const intl = useIntl();
  const [response, setResponse] = useState<ApiCollectionResponseJSON>();
  const [draft, setDraft] = useState<CollectionDraft>();
  const [addAccountId, setAddAccountId] = useState('');
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [actionFailed, setActionFailed] = useState(false);
  const [busyAction, setBusyAction] = useState<string>();

  const load = useCallback(async () => {
    const value = await apiGetCollection(match.params.id);
    setResponse(value);
    setDraft(draftFromResponse(value));
  }, [match.params.id]);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setFailed(false);
    apiGetCollection(match.params.id)
      .then((value) => {
        if (active) {
          setResponse(value);
          setDraft(draftFromResponse(value));
        }
        return undefined;
      })
      .catch(() => {
        if (active) setFailed(true);
        return undefined;
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [match.params.id]);

  const runAction = useCallback(
    async (
      actionName: string,
      action: () => Promise<unknown>,
      refresh = true,
    ) => {
      if (busyAction) return false;
      setBusyAction(actionName);
      setActionFailed(false);
      try {
        await action();
        if (refresh) await load();
        return true;
      } catch {
        setActionFailed(true);
        return false;
      } finally {
        setBusyAction(undefined);
      }
    },
    [busyAction, load],
  );

  const collection = response?.collection;
  const isOwner = Boolean(me && collection?.account_id === me);
  const accountsById = useMemo(
    () =>
      new Map(
        (response?.accounts ?? []).map((account) => [account.id, account]),
      ),
    [response?.accounts],
  );

  const updateDraft = useCallback(
    <Key extends keyof CollectionDraft>(
      key: Key,
      value: CollectionDraft[Key],
    ) => {
      setDraft((current) => (current ? { ...current, [key]: value } : current));
    },
    [],
  );

  const handleNameChange = useCallback<ChangeEventHandler<HTMLInputElement>>(
    (event) => {
      updateDraft('name', event.target.value);
    },
    [updateDraft],
  );
  const handleDescriptionChange = useCallback<
    ChangeEventHandler<HTMLTextAreaElement>
  >(
    (event) => {
      updateDraft('description', event.target.value);
    },
    [updateDraft],
  );
  const handleLanguageChange = useCallback<
    ChangeEventHandler<HTMLSelectElement>
  >(
    (event) => {
      updateDraft('language', event.target.value);
    },
    [updateDraft],
  );
  const handleTagNameChange = useCallback<ChangeEventHandler<HTMLInputElement>>(
    (event) => {
      updateDraft('tagName', event.target.value);
    },
    [updateDraft],
  );
  const handleSensitiveChange = useCallback<
    ChangeEventHandler<HTMLInputElement>
  >(
    (event) => {
      updateDraft('sensitive', event.target.checked);
    },
    [updateDraft],
  );
  const handleDiscoverableChange = useCallback<
    ChangeEventHandler<HTMLInputElement>
  >(
    (event) => {
      updateDraft('discoverable', event.target.checked);
    },
    [updateDraft],
  );
  const handleAddAccountIdChange = useCallback<
    ChangeEventHandler<HTMLInputElement>
  >((event) => {
    setAddAccountId(event.target.value);
  }, []);

  const handleSave = useCallback<FormEventHandler<HTMLFormElement>>(
    (event) => {
      event.preventDefault();
      if (!draft || !collection || !isOwner || !draft.name.trim()) return;
      const attributes: ApiCollectionUpdateJSON = {
        name: draft.name.trim(),
        description: draft.description.trim(),
        language: draft.language,
        sensitive: draft.sensitive,
        discoverable: draft.discoverable,
        tag_name: draft.tagName.trim(),
      };
      void runAction('save', () =>
        apiUpdateCollection(collection.id, attributes),
      );
    },
    [collection, draft, isOwner, runAction],
  );

  const handleAddItem = useCallback<FormEventHandler<HTMLFormElement>>(
    (event) => {
      event.preventDefault();
      const accountId = addAccountId.trim();
      if (!collection || !isOwner || !/^\d+$/.test(accountId)) return;
      void (async () => {
        const succeeded = await runAction('add-item', () =>
          apiAddCollectionItem(collection.id, accountId),
        );
        if (succeeded) setAddAccountId('');
      })();
    },
    [addAccountId, collection, isOwner, runAction],
  );

  const handleDelete = useCallback(() => {
    if (!collection || !isOwner) return;
    const confirmed = window.confirm(
      intl.formatMessage({
        id: 'collections.delete_confirm',
        defaultMessage: 'Delete this collection? This action cannot be undone.',
      }),
    );
    if (!confirmed) return;
    void (async () => {
      const succeeded = await runAction(
        'delete',
        () => apiDeleteCollection(collection.id),
        false,
      );
      if (succeeded) history.push('/collections');
    })();
  }, [collection, history, intl, isOwner, runAction]);

  const handleRemoveItem = useCallback(
    (itemId: string) => {
      if (!collection || !isOwner) return;
      void runAction(`remove-${itemId}`, () =>
        apiDeleteCollectionItem(collection.id, itemId),
      );
    },
    [collection, isOwner, runAction],
  );

  const handleRevokeItem = useCallback(
    (itemId: string) => {
      if (!collection || isOwner) return;
      void runAction(`revoke-${itemId}`, () =>
        apiRevokeCollectionItem(collection.id, itemId),
      );
    },
    [collection, isOwner, runAction],
  );

  return (
    <Column
      heading={intl.formatMessage({
        id: 'collections.title',
        defaultMessage: 'Collections',
      })}
    >
      <div className='paon-feature-page'>
        {failed && (
          <p role='alert'>
            <FormattedMessage
              id='collections.error'
              defaultMessage='Collections could not be loaded.'
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
        {actionFailed && (
          <p role='alert'>
            <FormattedMessage
              id='collections.action_error'
              defaultMessage='The collection could not be changed. Please try again.'
            />
          </p>
        )}
        {collection && draft && !loading && (
          <>
            <h1>{collection.name}</h1>
            {!isOwner && collection.description && (
              <p>{collection.description}</p>
            )}

            {isOwner && (
              <form
                className='paon-feature-page__form paon-feature-page__form--stacked'
                onSubmit={handleSave}
              >
                <label>
                  <FormattedMessage
                    id='collections.name'
                    defaultMessage='Name'
                  />
                  <input
                    type='text'
                    value={draft.name}
                    maxLength={40}
                    required
                    onChange={handleNameChange}
                  />
                </label>
                <label>
                  <FormattedMessage
                    id='collections.description'
                    defaultMessage='Description'
                  />
                  <textarea
                    value={draft.description}
                    maxLength={100}
                    onChange={handleDescriptionChange}
                  />
                </label>
                <label>
                  <FormattedMessage
                    id='collections.language'
                    defaultMessage='Language'
                  />
                  <select
                    value={draft.language}
                    onBlur={handleLanguageChange}
                    onChange={handleLanguageChange}
                  >
                    <option value=''>
                      <FormattedMessage
                        id='collections.language_any'
                        defaultMessage='Any language'
                      />
                    </option>
                    {(languages ?? []).map(([code, name, nativeName]) => (
                      <option key={code} value={code}>
                        {nativeName || name}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  <FormattedMessage
                    id='collections.tag_name'
                    defaultMessage='Associated hashtag'
                  />
                  <input
                    type='text'
                    value={draft.tagName}
                    placeholder='#mastodon'
                    onChange={handleTagNameChange}
                  />
                </label>
                <label className='paon-feature-page__checkbox'>
                  <input
                    type='checkbox'
                    checked={draft.sensitive}
                    onChange={handleSensitiveChange}
                  />
                  <FormattedMessage
                    id='collections.sensitive'
                    defaultMessage='Mark this collection as sensitive'
                  />
                </label>
                <label className='paon-feature-page__checkbox'>
                  <input
                    type='checkbox'
                    checked={draft.discoverable}
                    onChange={handleDiscoverableChange}
                  />
                  <FormattedMessage
                    id='collections.discoverable'
                    defaultMessage='Allow others to discover this collection'
                  />
                </label>
                <div className='paon-feature-page__actions'>
                  <Button
                    type='submit'
                    disabled={!draft.name.trim() || Boolean(busyAction)}
                    loading={busyAction === 'save'}
                  >
                    <FormattedMessage
                      id='collections.save'
                      defaultMessage='Save changes'
                    />
                  </Button>
                </div>
              </form>
            )}

            <section className='paon-feature-page__section'>
              <h2>
                <FormattedMessage
                  id='collections.accounts'
                  defaultMessage='Accounts'
                />
              </h2>
              {isOwner && (
                <form
                  className='paon-feature-page__form'
                  onSubmit={handleAddItem}
                >
                  <label>
                    <FormattedMessage
                      id='collections.account_id'
                      defaultMessage='Account ID'
                    />
                    <input
                      type='text'
                      inputMode='numeric'
                      pattern='[0-9]*'
                      value={addAccountId}
                      placeholder='123456789'
                      onChange={handleAddAccountIdChange}
                    />
                  </label>
                  <Button
                    type='submit'
                    disabled={
                      !/^\d+$/.test(addAccountId.trim()) || Boolean(busyAction)
                    }
                    loading={busyAction === 'add-item'}
                  >
                    <FormattedMessage
                      id='collections.add_account'
                      defaultMessage='Add account'
                    />
                  </Button>
                </form>
              )}
              {collection.items.length === 0 ? (
                <p>
                  <FormattedMessage
                    id='collections.accounts_empty'
                    defaultMessage='No accounts in this collection.'
                  />
                </p>
              ) : (
                <ul className='paon-feature-page__list paon-feature-page__list--accounts'>
                  {collection.items.map((item) => {
                    const account = item.account_id
                      ? accountsById.get(item.account_id)
                      : undefined;
                    const canRevoke = Boolean(
                      !isOwner &&
                        me &&
                        item.account_id === me &&
                        (item.state === 'pending' || item.state === 'accepted'),
                    );
                    return (
                      <CollectionItemRow
                        key={item.id}
                        item={item}
                        account={account}
                        isOwner={isOwner}
                        canRevoke={canRevoke}
                        busyAction={busyAction}
                        onRemove={handleRemoveItem}
                        onRevoke={handleRevokeItem}
                      />
                    );
                  })}
                </ul>
              )}
            </section>

            {isOwner && (
              <section className='paon-feature-page__section paon-feature-page__danger-zone'>
                <h2>
                  <FormattedMessage
                    id='collections.delete_title'
                    defaultMessage='Delete collection'
                  />
                </h2>
                <p>
                  <FormattedMessage
                    id='collections.delete_description'
                    defaultMessage='Permanently delete this collection and remove all of its accounts.'
                  />
                </p>
                <Button
                  dangerous
                  disabled={Boolean(busyAction)}
                  loading={busyAction === 'delete'}
                  onClick={handleDelete}
                >
                  <FormattedMessage
                    id='collections.delete'
                    defaultMessage='Delete collection'
                  />
                </Button>
              </section>
            )}
          </>
        )}
      </div>
    </Column>
  );
};

export default CollectionDetail;
