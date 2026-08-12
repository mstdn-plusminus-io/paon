import { connect } from 'react-redux';

import {
  changeSearch,
  clearSearch,
  submitSearch,
  showSearch,
  openURL,
  clickSearchResult,
  forgetSearchResult,
} from 'mastodon/actions/search';

import Search from '../components/search';

const mapStateToProps = state => ({
  value: state.getIn(['search', 'value']),
  submitted: state.getIn(['search', 'submitted']),
  recent: state.getIn(['search', 'recent']).reverse(),
});

const mapDispatchToProps = dispatch => ({

  onChange (value) {
    dispatch(changeSearch(value));
  },

  onClear () {
    dispatch(clearSearch());
  },

  onSubmit (type, value) {
    dispatch(submitSearch(type, value));
  },

  onShow () {
    dispatch(showSearch());
  },

  onOpenURL (q, routerHistory) {
    dispatch(openURL(q, routerHistory));
  },

  onClickSearchResult (q, type) {
    dispatch(clickSearchResult(q, type));
  },

  onForgetSearchResult (search) {
    dispatch(forgetSearchResult(search));
  },

});

export default connect(mapStateToProps, mapDispatchToProps)(Search);
