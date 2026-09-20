import { fromJS } from 'immutable';

import { isStatusQuoteable } from '../status_quote';

const status = overrides => fromJS({
  visibility: 'public',
  reblog: null,
  account: {
    id: '1',
    suspended: false,
    moved: null,
  },
  ...overrides,
});

describe('isStatusQuoteable', () => {
  it.each(['public', 'unlisted'])('allows an available %s original post', visibility => {
    expect(isStatusQuoteable(status({ visibility }), fromJS({}), true)).toBe(true);
  });

  it.each(['private', 'direct'])('rejects %s visibility', visibility => {
    expect(isStatusQuoteable(status({ visibility }), fromJS({}), true)).toBe(false);
  });

  it('rejects reblogs, signed-out viewers, and unavailable accounts', () => {
    expect(isStatusQuoteable(status({ reblog: { id: '2' } }), fromJS({}), true)).toBe(false);
    expect(isStatusQuoteable(status({}), fromJS({}), false)).toBe(false);
    expect(isStatusQuoteable(status({ account: { suspended: true } }), fromJS({}), true)).toBe(false);
    expect(isStatusQuoteable(status({ account: { moved: { id: '2' } } }), fromJS({}), true)).toBe(false);
  });

  it.each(['blocking', 'blocked_by', 'muting', 'domain_blocking'])('rejects a %s relationship', relationship => {
    expect(isStatusQuoteable(status({}), fromJS({ [relationship]: true }), true)).toBe(false);
  });
});
