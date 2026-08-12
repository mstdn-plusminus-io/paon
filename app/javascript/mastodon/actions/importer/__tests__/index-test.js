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
});
