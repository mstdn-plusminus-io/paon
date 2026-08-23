import { createIntl, IntlProvider } from 'react-intl';

import { fromJS } from 'immutable';

import { act, fireEvent, render, screen } from '@testing-library/react';

import { Poll } from '../poll';

const intl = createIntl({ locale: 'en' });

describe('Mastodon 4.4 logged-out poll interaction', () => {
  afterEach(() => {
    jest.useRealTimers();
  });

  it('opens the interaction modal instead of disabling voting', () => {
    const onInteractionModal = jest.fn();
    const onVote = jest.fn();
    const status = fromJS({
      id: 'status-1',
      uri: 'https://example.com/@alice/1',
      account: { id: 'account-1' },
    });
    const poll = fromJS({
      id: 'poll-1',
      emojis: [],
      options: [
        { title: 'Choice A', votes_count: 0 },
        { title: 'Choice B', votes_count: 0 },
      ],
      multiple: false,
      voted: false,
      own_votes: [],
      expired: false,
      expires_at: '2999-01-01T00:00:00.000Z',
      voters_count: 0,
      votes_count: 0,
    });

    render(
      <IntlProvider locale='en'>
        <Poll
          identity={{ signedIn: false, permissions: 0 }}
          intl={intl}
          poll={poll}
          status={status}
          onVote={onVote}
          onInteractionModal={onInteractionModal}
        />
      </IntlProvider>,
    );

    fireEvent.click(screen.getAllByRole('radio')[0]);
    fireEvent.click(screen.getByRole('button', { name: 'Vote' }));

    expect(onVote).not.toHaveBeenCalled();
    expect(onInteractionModal).toHaveBeenCalledWith('vote', status);
  });

  it('keeps a poll without an expiration date open for voting', () => {
    jest.useFakeTimers();

    const onInteractionModal = jest.fn();
    const onVote = jest.fn();
    const status = fromJS({
      id: 'status-1',
      uri: 'https://example.com/@alice/1',
      account: { id: 'account-1' },
    });
    const poll = fromJS({
      id: 'poll-1',
      emojis: [],
      options: [
        { title: 'Choice A', votes_count: 0 },
        { title: 'Choice B', votes_count: 0 },
      ],
      multiple: false,
      voted: false,
      own_votes: [],
      expired: false,
      expires_at: null,
      voters_count: 0,
      votes_count: 0,
    });

    render(
      <IntlProvider locale='en'>
        <Poll
          identity={{ signedIn: true, permissions: 0 }}
          intl={intl}
          poll={poll}
          status={status}
          onVote={onVote}
          onInteractionModal={onInteractionModal}
        />
      </IntlProvider>,
    );

    act(() => jest.runOnlyPendingTimers());
    fireEvent.click(screen.getAllByRole('radio')[0]);
    fireEvent.click(screen.getByRole('button', { name: 'Vote' }));

    expect(onVote).toHaveBeenCalledWith(['0']);
  });
});
