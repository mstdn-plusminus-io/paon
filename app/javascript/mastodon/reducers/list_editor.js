import { Map as ImmutableMap, List as ImmutableList, Set as ImmutableSet } from 'immutable';

import {
  LIST_CREATE_REQUEST,
  LIST_CREATE_FAIL,
  LIST_CREATE_SUCCESS,
  LIST_UPDATE_REQUEST,
  LIST_UPDATE_FAIL,
  LIST_UPDATE_SUCCESS,
  LIST_EDITOR_RESET,
  LIST_EDITOR_SETUP,
  LIST_EDITOR_TITLE_CHANGE,
  LIST_ACCOUNTS_FETCH_REQUEST,
  LIST_ACCOUNTS_FETCH_SUCCESS,
  LIST_ACCOUNTS_FETCH_FAIL,
  LIST_EDITOR_SUGGESTIONS_READY,
  LIST_EDITOR_SUGGESTIONS_REQUEST,
  LIST_EDITOR_SUGGESTIONS_FAIL,
  LIST_EDITOR_SUGGESTIONS_CLEAR,
  LIST_EDITOR_SUGGESTIONS_CHANGE,
  LIST_EDITOR_ADD_REQUEST,
  LIST_EDITOR_ADD_SUCCESS,
  LIST_EDITOR_ADD_FAIL,
  LIST_EDITOR_REMOVE_REQUEST,
  LIST_EDITOR_REMOVE_SUCCESS,
  LIST_EDITOR_REMOVE_FAIL,
} from '../actions/lists';

const initialState = ImmutableMap({
  listId: null,
  isSubmitting: false,
  isChanged: false,
  title: '',
  isExclusive: false,

  accounts: ImmutableMap({
    items: ImmutableList(),
    loaded: false,
    isLoading: false,
    error: false,
    pending: ImmutableSet(),
  }),

  suggestions: ImmutableMap({
    value: '',
    items: ImmutableList(),
    isLoading: false,
    loaded: false,
    error: false,
  }),
});

export default function listEditorReducer(state = initialState, action) {
  switch(action.type) {
  case LIST_EDITOR_RESET:
    return initialState;
  case LIST_EDITOR_SETUP:
    return state.withMutations(map => {
      map.set('listId', action.list.get('id'));
      map.set('title', action.list.get('title'));
      map.set('isExclusive', action.list.get('is_exclusive'));
      map.set('isSubmitting', false);
    });
  case LIST_EDITOR_TITLE_CHANGE:
    return state.withMutations(map => {
      map.set('title', action.value);
      map.set('isChanged', true);
    });
  case LIST_CREATE_REQUEST:
  case LIST_UPDATE_REQUEST:
    return state.withMutations(map => {
      map.set('isSubmitting', true);
      map.set('isChanged', false);
    });
  case LIST_CREATE_FAIL:
  case LIST_UPDATE_FAIL:
    return state.set('isSubmitting', false);
  case LIST_CREATE_SUCCESS:
  case LIST_UPDATE_SUCCESS:
    return state.withMutations(map => {
      map.set('isSubmitting', false);
      map.set('listId', action.list.id);
    });
  case LIST_ACCOUNTS_FETCH_REQUEST:
    return state.update('accounts', accounts => accounts
      .set('isLoading', true)
      .set('error', false));
  case LIST_ACCOUNTS_FETCH_FAIL:
    return state.update('accounts', accounts => accounts
      .set('isLoading', false)
      .set('loaded', true)
      .set('error', true));
  case LIST_ACCOUNTS_FETCH_SUCCESS:
    return state.update('accounts', accounts => accounts.withMutations(map => {
      map.set('isLoading', false);
      map.set('loaded', true);
      map.set('error', false);
      map.set('items', ImmutableList(action.accounts.map(item => item.id)));
    }));
  case LIST_EDITOR_SUGGESTIONS_CHANGE:
    return state.update('suggestions', suggestions => suggestions
      .set('value', action.value)
      .set('items', ImmutableList())
      .set('loaded', false)
      .set('error', false));
  case LIST_EDITOR_SUGGESTIONS_REQUEST:
    return state.update('suggestions', suggestions => suggestions
      .set('isLoading', true)
      .set('loaded', false)
      .set('error', false));
  case LIST_EDITOR_SUGGESTIONS_READY:
    if (state.getIn(['suggestions', 'value']).trim() !== action.query) {
      return state;
    }

    return state.update('suggestions', suggestions => suggestions
      .set('items', ImmutableList(action.accounts.map(item => item.id)))
      .set('isLoading', false)
      .set('loaded', true)
      .set('error', false));
  case LIST_EDITOR_SUGGESTIONS_FAIL:
    if (state.getIn(['suggestions', 'value']).trim() !== action.query) {
      return state;
    }

    return state.update('suggestions', suggestions => suggestions
      .set('items', ImmutableList())
      .set('isLoading', false)
      .set('loaded', true)
      .set('error', true));
  case LIST_EDITOR_SUGGESTIONS_CLEAR:
    return state.update('suggestions', suggestions => suggestions.withMutations(map => {
      map.set('items', ImmutableList());
      map.set('value', '');
      map.set('isLoading', false);
      map.set('loaded', false);
      map.set('error', false);
    }));
  case LIST_EDITOR_ADD_REQUEST:
  case LIST_EDITOR_REMOVE_REQUEST:
    return state.updateIn(['accounts', 'pending'], pending => pending.add(action.accountId));
  case LIST_EDITOR_ADD_SUCCESS:
    return state.update('accounts', accounts => accounts.withMutations(map => {
      map.update('pending', pending => pending.delete(action.accountId));
      map.update('items', list => list.includes(action.accountId) ? list : list.unshift(action.accountId));
    }));
  case LIST_EDITOR_REMOVE_SUCCESS:
    return state.update('accounts', accounts => accounts.withMutations(map => {
      map.update('pending', pending => pending.delete(action.accountId));
      map.update('items', list => list.filterNot(item => item === action.accountId));
    }));
  case LIST_EDITOR_ADD_FAIL:
  case LIST_EDITOR_REMOVE_FAIL:
    return state.updateIn(['accounts', 'pending'], pending => pending.delete(action.accountId));
  default:
    return state;
  }
}
