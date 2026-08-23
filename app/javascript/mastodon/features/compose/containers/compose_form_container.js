import { connect } from 'react-redux';

import {
  changeCompose,
  submitCompose,
  clearComposeSuggestions,
  fetchComposeSuggestions,
  selectComposeSuggestion,
  changeComposeSpoilerText,
  insertEmojiCompose,
  uploadCompose,
  changeComposeVisibility,
  setComposeInstanceLimits,
  pasteLinkCompose,
} from '../../../actions/compose';
import { openModal } from '../../../actions/modal';
import ComposeForm from '../components/compose_form';
import { getComposeSuccessRedirect } from '../util/compose_redirect';
import { findMissingAltTextMediaId } from '../util/missing_alt_text';

const mapStateToProps = state => ({
  text: state.getIn(['compose', 'text']),
  suggestions: state.getIn(['compose', 'suggestions']),
  spoiler: state.getIn(['compose', 'spoiler']),
  spoilerText: state.getIn(['compose', 'spoiler_text']),
  privacy: state.getIn(['compose', 'privacy']),
  focusDate: state.getIn(['compose', 'focusDate']),
  caretPosition: state.getIn(['compose', 'caretPosition']),
  preselectDate: state.getIn(['compose', 'preselectDate']),
  isSubmitting: state.getIn(['compose', 'is_submitting']),
  isEditing: state.getIn(['compose', 'id']) !== null,
  isChangingUpload: state.getIn(['compose', 'is_changing_upload']),
  isUploading: state.getIn(['compose', 'is_uploading']),
  anyMedia: state.getIn(['compose', 'media_attachments']).size > 0,
  missingAltTextMediaId: findMissingAltTextMediaId(state.getIn(['compose', 'media_attachments'])),
  isInReply: state.getIn(['compose', 'in_reply_to']) !== null,
  lang: state.getIn(['compose', 'language']),
  maxChars: state.getIn(['compose', 'max_characters']),
  pollOptions: state.getIn(['compose', 'poll', 'options']),
  maxPollOptions: state.getIn(['compose', 'max_poll_options']),
  maxPollOptionCharacters: state.getIn(['compose', 'max_poll_option_characters']),
});

let cachedKeywordVisibilities = null;

const mapDispatchToProps = (dispatch, props) => ({
  onInitialize(instance) {
    dispatch(setComposeInstanceLimits(instance.configuration));
  },

  onChange (text) {
    dispatch(changeCompose(text));

    if (localStorage.plusminus_config_keyword_based_visibility === 'enabled') {
      if (!cachedKeywordVisibilities) {
        cachedKeywordVisibilities = JSON.parse(localStorage.plusminus_config_keyword_based_visibilities);
      }
      const matched = cachedKeywordVisibilities.find((option) => text.includes(option.keyword));
      if (matched) {
        dispatch(changeComposeVisibility(matched.visibility));
      }
    }
  },

  onSubmit (router, missingAltTextMediaId) {
    if (missingAltTextMediaId) {
      dispatch(openModal({
        modalType: 'CONFIRM_MISSING_ALT_TEXT',
        modalProps: { id: missingAltTextMediaId, router },
      }));
    } else {
      dispatch(submitCompose(router, status => {
        const redirectURL = getComposeSuccessRedirect(props.redirectOnSuccess, status);

        if (redirectURL) {
          window.location.assign(redirectURL);
        }
      }));
    }
  },

  onClearSuggestions () {
    dispatch(clearComposeSuggestions());
  },

  onFetchSuggestions (token) {
    dispatch(fetchComposeSuggestions(token));
  },

  onSuggestionSelected (position, token, suggestion, path) {
    dispatch(selectComposeSuggestion(position, token, suggestion, path));
  },

  onChangeSpoilerText (checked) {
    dispatch(changeComposeSpoilerText(checked));

    if (localStorage.plusminus_config_keyword_based_visibility === 'enabled' && localStorage.plusminus_config_spoiler_keyword_based_visibility === 'enabled') {
      if (!cachedKeywordVisibilities) {
        cachedKeywordVisibilities = JSON.parse(localStorage.plusminus_config_keyword_based_visibilities);
      }
      const matched = cachedKeywordVisibilities.find((option) => checked.includes(option.keyword));
      if (matched) {
        dispatch(changeComposeVisibility(matched.visibility));
      }
    }
  },

  onPaste (event) {
    if (event.clipboardData?.files.length === 1) {
      dispatch(uploadCompose(event.clipboardData.files));
      event.preventDefault();
      return;
    }

    const text = event.clipboardData?.getData('text/plain')?.trim();
    if (!/^https?:\/\/[^\s]+\/[^\s]+$/i.test(text || '')) return;

    try {
      dispatch(pasteLinkCompose(new URL(text).toString()));
    } catch {
      // Keep malformed links as ordinary compose text.
    }
  },

  onPickEmoji (position, data, needsSpace) {
    dispatch(insertEmojiCompose(position, data, needsSpace));
  },

});

export default connect(mapStateToProps, mapDispatchToProps)(ComposeForm);
