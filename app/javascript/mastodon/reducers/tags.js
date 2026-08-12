import { Map as ImmutableMap, fromJS } from 'immutable';

import {
  HASHTAG_FETCH_SUCCESS,
  HASHTAG_FOLLOW_REQUEST,
  HASHTAG_FOLLOW_FAIL,
  HASHTAG_UNFOLLOW_REQUEST,
  HASHTAG_UNFOLLOW_FAIL,
  HASHTAG_FEATURE_REQUEST,
  HASHTAG_FEATURE_SUCCESS,
  HASHTAG_FEATURE_FAIL,
  HASHTAG_UNFEATURE_REQUEST,
  HASHTAG_UNFEATURE_SUCCESS,
  HASHTAG_UNFEATURE_FAIL,
} from 'mastodon/actions/tags';

const initialState = ImmutableMap();

export default function tags(state = initialState, action) {
  switch(action.type) {
  case HASHTAG_FETCH_SUCCESS:
    return state.set(action.name, fromJS(action.tag));
  case HASHTAG_FOLLOW_REQUEST:
  case HASHTAG_UNFOLLOW_FAIL:
    return state.setIn([action.name, 'following'], true);
  case HASHTAG_FOLLOW_FAIL:
  case HASHTAG_UNFOLLOW_REQUEST:
    return state.setIn([action.name, 'following'], false);
  case HASHTAG_FEATURE_REQUEST:
  case HASHTAG_UNFEATURE_FAIL:
    return state.setIn([action.name, 'featuring'], true);
  case HASHTAG_FEATURE_FAIL:
  case HASHTAG_UNFEATURE_REQUEST:
    return state.setIn([action.name, 'featuring'], false);
  case HASHTAG_FEATURE_SUCCESS:
  case HASHTAG_UNFEATURE_SUCCESS:
    return state.set(action.name, fromJS(action.tag));
  default:
    return state;
  }
}
