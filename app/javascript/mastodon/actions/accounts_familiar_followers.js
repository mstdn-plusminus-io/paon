import api from '../api';

import { importFetchedAccounts } from './importer';

export const ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_REQUEST = 'ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_REQUEST';
export const ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_SUCCESS = 'ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_SUCCESS';
export const ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_FAIL = 'ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_FAIL';

export const normalizeFamiliarFollowersResponse = (id, data) => {
  const result = Array.isArray(data) ? data.find(item => String(item?.id) === String(id)) : null;

  return {
    id: String(id),
    accounts: Array.isArray(result?.accounts) ? result.accounts : [],
  };
};

export const fetchAccountsFamiliarFollowers = id => (dispatch, getState) => {
  const state = getState();
  const accountId = String(id);
  const currentAccountId = state.getIn(['meta', 'me']);

  if (!currentAccountId || accountId === String(currentAccountId) || state.getIn(['accounts_familiar_followers', accountId, 'loaded']) || state.getIn(['accounts_familiar_followers', accountId, 'isLoading'])) {
    return Promise.resolve();
  }

  dispatch({ type: ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_REQUEST, id: accountId });

  return api().get('/api/v1/accounts/familiar_followers', { params: { id: accountId } }).then(({ data }) => {
    const result = normalizeFamiliarFollowersResponse(accountId, data);

    dispatch(importFetchedAccounts(result.accounts));
    dispatch({
      type: ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_SUCCESS,
      id: result.id,
      accountIds: result.accounts.map(account => account.id),
    });
  }).catch(error => dispatch({
    type: ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_FAIL,
    id: accountId,
    error,
    skipAlert: true,
  }));
};
