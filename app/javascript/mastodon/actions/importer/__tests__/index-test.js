import { importFetchedStatuses, ACCOUNTS_IMPORT, STATUSES_IMPORT } from '..';

jest.mock('../../../features/emoji/emoji', () => jest.fn(value => value));
jest.mock('../../../initial_state', () => ({ expandSpoilers: false }));

const account = (id, username) => ({
  id,
  username,
  display_name: username,
  emojis: [],
  fields: [],
  note: '',
  url: `https://example.com/@${username}`,
  uri: `https://example.com/users/${username}`,
});

describe('importFetchedStatuses', () => {
  it('imports preview-card authors and stores only their account IDs on the status', () => {
    const statusAccount = account('1', 'publisher');
    const authorAccount = account('2', 'author');
    const dispatch = jest.fn();
    const state = { getIn: jest.fn(() => undefined) };

    importFetchedStatuses([
      {
        id: 'status-1',
        account: statusAccount,
        card: {
          authors: [{ name: 'Author', account: authorAccount }],
        },
        content: '',
        emojis: [],
        filtered: [],
        media_attachments: [],
        poll: null,
        reblog: null,
        sensitive: false,
        spoiler_text: '',
        uri: 'https://example.com/statuses/1',
        url: 'https://example.com/statuses/1',
      },
    ])(dispatch, () => state);

    const accountImport = dispatch.mock.calls
      .map(([action]) => action)
      .find(action => action.type === ACCOUNTS_IMPORT);
    const statusImport = dispatch.mock.calls
      .map(([action]) => action)
      .find(action => action.type === STATUSES_IMPORT);

    expect(accountImport.accounts.map(imported => imported.id)).toEqual([
      statusAccount.id,
      authorAccount.id,
    ]);
    expect(statusImport.statuses[0].card.authors[0]).toEqual({
      name: 'Author',
      accountId: authorAccount.id,
      account: undefined,
    });
  });

  it('imports and normalizes an embedded Mastodon quote', () => {
    const statusAccount = account('1', 'publisher');
    const quotedAccount = account('2', 'quoted');
    const dispatch = jest.fn();
    const state = { getIn: jest.fn(() => undefined) };
    const baseStatus = {
      card: null,
      emojis: [],
      filtered: [],
      media_attachments: [],
      poll: null,
      reblog: null,
      sensitive: false,
      spoiler_text: '',
    };

    importFetchedStatuses([
      {
        ...baseStatus,
        id: 'status-1',
        account: statusAccount,
        content: '<p>My comment</p><p class="quote-inline"><a href="https://example.com/@quoted/2">RE: quote</a></p>',
        quote: {
          state: 'accepted',
          quoted_status: {
            ...baseStatus,
            id: 'status-2',
            account: quotedAccount,
            content: '<p>Quoted post</p>',
            uri: 'https://example.com/statuses/2',
            url: 'https://example.com/@quoted/2',
          },
        },
        uri: 'https://example.com/statuses/1',
        url: 'https://example.com/@publisher/1',
      },
    ])(dispatch, () => state);

    const accountImport = dispatch.mock.calls
      .map(([action]) => action)
      .find(action => action.type === ACCOUNTS_IMPORT);
    const statusImport = dispatch.mock.calls
      .map(([action]) => action)
      .find(action => action.type === STATUSES_IMPORT);
    const parent = statusImport.statuses.find(status => status.id === 'status-1');

    expect(accountImport.accounts.map(imported => imported.id)).toEqual(['1', '2']);
    expect(statusImport.statuses.map(status => status.id)).toEqual(['status-1', 'status-2']);
    expect(parent.quote).toEqual({
      state: 'accepted',
      quoted_status: 'status-2',
    });
    expect(parent.contentHtml).toBe('<p>My comment</p>');
  });

  it('drops the non-personalized quote policy received from public streams', () => {
    const dispatch = jest.fn();
    const state = { getIn: jest.fn(() => undefined) };

    importFetchedStatuses([
      {
        id: 'status-1',
        account: account('1', 'publisher'),
        card: null,
        content: '<p>Post</p>',
        emojis: [],
        filtered: [],
        media_attachments: [],
        poll: null,
        quote_approval: { current_user: 'automatic' },
        reblog: null,
        sensitive: false,
        spoiler_text: '',
        uri: 'https://example.com/statuses/1',
        url: 'https://example.com/@publisher/1',
      },
    ], { bogusQuotePolicy: true })(dispatch, () => state);

    const statusImport = dispatch.mock.calls
      .map(([action]) => action)
      .find(action => action.type === STATUSES_IMPORT);

    expect(statusImport.statuses[0].quote_approval).toBeNull();
  });
});
