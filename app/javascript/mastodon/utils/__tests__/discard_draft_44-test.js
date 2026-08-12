import { fromJS } from 'immutable';

import { discardDraftModalProps, hasComposeDraft } from '../discard_draft';

describe('Mastodon 4.4 discard draft confirmation', () => {
  const intl = { formatMessage: message => message.defaultMessage };

  it('detects text, media, and poll drafts', () => {
    const state = compose => fromJS({ compose: { text: '', media_attachments: [], poll: null, ...compose } });

    expect(hasComposeDraft(state({}))).toBe(false);
    expect(hasComposeDraft(state({ text: 'draft' }))).toBe(true);
    expect(hasComposeDraft(state({ media_attachments: [{ id: '1' }] }))).toBe(true);
    expect(hasComposeDraft(state({ poll: { options: [] } }))).toBe(true);
  });

  it('uses contextual resume and discard wording', () => {
    expect(discardDraftModalProps(intl, false)).toMatchObject({
      title: 'Discard your draft post?',
      cancel: 'Resume draft',
      confirm: 'Discard and continue',
    });
    expect(discardDraftModalProps(intl, true)).toMatchObject({
      title: 'Discard changes to your post?',
      cancel: 'Resume editing',
    });
  });
});
