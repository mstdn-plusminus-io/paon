import { connect } from 'react-redux';

import {
  removePollOption,
  changePollOption,
  changePollSettings,
  clearComposeSuggestions,
  fetchComposeSuggestions,
  selectComposeSuggestion,
} from '../../../actions/compose';
import PollForm from '../components/poll_form';

const mapStateToProps = state => ({
  suggestions: state.getIn(['compose', 'suggestions']),
  options: state.getIn(['compose', 'poll', 'options']),
  lang: state.getIn(['compose', 'language']),
  expiresIn: state.getIn(['compose', 'poll', 'expires_in']),
  isMultiple: state.getIn(['compose', 'poll', 'multiple']),
  maxOptions: state.getIn(['compose', 'max_poll_options']),
  maxCharacters: state.getIn(['compose', 'max_poll_option_characters']),
});

const mapDispatchToProps = dispatch => ({
  onRemoveOption(index) {
    dispatch(removePollOption(index));
  },

  onChangeOption(index, title, maxOptions) {
    dispatch(changePollOption(index, title, maxOptions));
  },

  onChangeSettings(expiresIn, isMultiple) {
    dispatch(changePollSettings(expiresIn, isMultiple));
  },

  onClearSuggestions () {
    dispatch(clearComposeSuggestions());
  },

  onFetchSuggestions (token) {
    dispatch(fetchComposeSuggestions(token));
  },

  onSuggestionSelected (position, token, accountId, path) {
    dispatch(selectComposeSuggestion(position, token, accountId, path));
  },

});

export default connect(mapStateToProps, mapDispatchToProps)(PollForm);
