import { List as ImmutableList, Map as ImmutableMap } from 'immutable';

import {
  ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_REQUEST,
  ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_SUCCESS,
  ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_FAIL,
} from '../actions/accounts_familiar_followers';

const emptyResult = ImmutableMap({
  accountIds: ImmutableList(),
  isLoading: false,
  loaded: false,
  error: null,
});

export default function accountsFamiliarFollowersReducer(state = ImmutableMap(), action) {
  switch (action.type) {
  case ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_REQUEST:
    return state.set(action.id, emptyResult.merge({ isLoading: true }));
  case ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_SUCCESS:
    return state.set(action.id, emptyResult.merge({
      accountIds: ImmutableList(action.accountIds),
      loaded: true,
    }));
  case ACCOUNTS_FAMILIAR_FOLLOWERS_FETCH_FAIL:
    return state.set(action.id, emptyResult.set('error', action.error));
  default:
    return state;
  }
}
