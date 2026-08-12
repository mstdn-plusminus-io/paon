import { Map as ImmutableMap, List as ImmutableList, Set as ImmutableSet } from 'immutable';

import {
  LIST_ADDER_RESET,
  LIST_ADDER_SETUP,
  LIST_ADDER_LISTS_FETCH_REQUEST,
  LIST_ADDER_LISTS_FETCH_SUCCESS,
  LIST_ADDER_LISTS_FETCH_FAIL,
  LIST_EDITOR_ADD_REQUEST,
  LIST_EDITOR_ADD_SUCCESS,
  LIST_EDITOR_ADD_FAIL,
  LIST_EDITOR_REMOVE_REQUEST,
  LIST_EDITOR_REMOVE_SUCCESS,
  LIST_EDITOR_REMOVE_FAIL,
} from '../actions/lists';

const initialState = ImmutableMap({
  accountId: null,

  lists: ImmutableMap({
    items: ImmutableList(),
    loaded: false,
    isLoading: false,
    error: false,
    pending: ImmutableSet(),
  }),
});

export default function listAdderReducer(state = initialState, action) {
  switch(action.type) {
  case LIST_ADDER_RESET:
    return initialState;
  case LIST_ADDER_SETUP:
    return state.withMutations(map => {
      map.set('accountId', action.account.get('id'));
    });
  case LIST_ADDER_LISTS_FETCH_REQUEST:
    return state.update('lists', lists => lists
      .set('isLoading', true)
      .set('error', false));
  case LIST_ADDER_LISTS_FETCH_FAIL:
    return state.update('lists', lists => lists
      .set('isLoading', false)
      .set('loaded', true)
      .set('error', true));
  case LIST_ADDER_LISTS_FETCH_SUCCESS:
    return state.update('lists', lists => lists.withMutations(map => {
      map.set('isLoading', false);
      map.set('loaded', true);
      map.set('error', false);
      map.set('items', ImmutableList(action.lists.map(item => item.id)));
    }));
  case LIST_EDITOR_ADD_REQUEST:
  case LIST_EDITOR_REMOVE_REQUEST:
    return state.updateIn(['lists', 'pending'], pending => pending.add(action.listId));
  case LIST_EDITOR_ADD_SUCCESS:
    return state.update('lists', lists => lists.withMutations(map => {
      map.update('pending', pending => pending.delete(action.listId));
      map.update('items', list => list.includes(action.listId) ? list : list.unshift(action.listId));
    }));
  case LIST_EDITOR_REMOVE_SUCCESS:
    return state.update('lists', lists => lists.withMutations(map => {
      map.update('pending', pending => pending.delete(action.listId));
      map.update('items', list => list.filterNot(item => item === action.listId));
    }));
  case LIST_EDITOR_ADD_FAIL:
  case LIST_EDITOR_REMOVE_FAIL:
    return state.updateIn(['lists', 'pending'], pending => pending.delete(action.listId));
  default:
    return state;
  }
}
