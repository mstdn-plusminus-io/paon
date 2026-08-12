import api from '../api';

import { importFetchedAccounts } from './importer';

export const FEATURED_TAGS_FETCH_REQUEST = 'FEATURED_TAGS_FETCH_REQUEST';
export const FEATURED_TAGS_FETCH_SUCCESS = 'FEATURED_TAGS_FETCH_SUCCESS';
export const FEATURED_TAGS_FETCH_FAIL    = 'FEATURED_TAGS_FETCH_FAIL';

export const FEATURED_ACCOUNTS_FETCH_REQUEST = 'FEATURED_ACCOUNTS_FETCH_REQUEST';
export const FEATURED_ACCOUNTS_FETCH_SUCCESS = 'FEATURED_ACCOUNTS_FETCH_SUCCESS';
export const FEATURED_ACCOUNTS_FETCH_FAIL    = 'FEATURED_ACCOUNTS_FETCH_FAIL';

export const fetchFeaturedTags = (id) => (dispatch, getState) => {
  if (getState().getIn(['user_lists', 'featured_tags', id, 'items'])) {
    return;
  }

  dispatch(fetchFeaturedTagsRequest(id));

  api().get(`/api/v1/accounts/${id}/featured_tags`)
    .then(({ data }) => dispatch(fetchFeaturedTagsSuccess(id, data)))
    .catch(err => dispatch(fetchFeaturedTagsFail(id, err)));
};

export const fetchFeaturedTagsRequest = (id) => ({
  type: FEATURED_TAGS_FETCH_REQUEST,
  id,
});

export const fetchFeaturedTagsSuccess = (id, tags) => ({
  type: FEATURED_TAGS_FETCH_SUCCESS,
  id,
  tags,
});

export const fetchFeaturedTagsFail = (id, error) => ({
  type: FEATURED_TAGS_FETCH_FAIL,
  id,
  error,
});

export const fetchFeaturedAccounts = (id) => (dispatch, getState) => {
  if (getState().getIn(['user_lists', 'featured_accounts', id, 'items'])) {
    return;
  }

  dispatch({ type: FEATURED_ACCOUNTS_FETCH_REQUEST, id });

  api().get(`/api/v1/accounts/${id}/endorsements`)
    .then(({ data }) => {
      dispatch(importFetchedAccounts(data));
      dispatch({ type: FEATURED_ACCOUNTS_FETCH_SUCCESS, id, accounts: data });
    })
    .catch(error => dispatch({ type: FEATURED_ACCOUNTS_FETCH_FAIL, id, error }));
};
