import { defineMessages } from 'react-intl';

const editMessages = defineMessages({
  title: { id: 'confirmations.discard_draft.edit.title', defaultMessage: 'Discard changes to your post?' },
  message: { id: 'confirmations.discard_draft.edit.message', defaultMessage: 'Continuing will discard any changes you have made to the post you are currently editing.' },
  cancel: { id: 'confirmations.discard_draft.edit.cancel', defaultMessage: 'Resume editing' },
});

const postMessages = defineMessages({
  title: { id: 'confirmations.discard_draft.post.title', defaultMessage: 'Discard your draft post?' },
  message: { id: 'confirmations.discard_draft.post.message', defaultMessage: 'Continuing will discard the post you are currently composing.' },
  cancel: { id: 'confirmations.discard_draft.post.cancel', defaultMessage: 'Resume draft' },
});

const messages = defineMessages({
  confirm: { id: 'confirmations.discard_draft.confirm', defaultMessage: 'Discard and continue' },
});

export const hasComposeDraft = state => (
  state.getIn(['compose', 'text']).trim().length !== 0 ||
  state.getIn(['compose', 'media_attachments']).size > 0 ||
  state.getIn(['compose', 'poll']) !== null
);

export const discardDraftModalProps = (intl, isEditing) => {
  const contextualMessages = isEditing ? editMessages : postMessages;

  return {
    title: intl.formatMessage(contextualMessages.title),
    message: intl.formatMessage(contextualMessages.message),
    cancel: intl.formatMessage(contextualMessages.cancel),
    confirm: intl.formatMessage(messages.confirm),
  };
};
