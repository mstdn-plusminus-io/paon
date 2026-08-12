import { IntlProvider } from 'react-intl';

import { fromJS } from 'immutable';

import { render, screen } from '@testing-library/react';

import { AnnouncementTimestamp } from '../announcement_timestamp';

describe('Mastodon 4.4 announcement timestamps', () => {
  it('shows published_at when no scheduled time range exists', () => {
    render(
      <IntlProvider locale='en' timeZone='UTC'>
        <AnnouncementTimestamp
          announcement={fromJS({ published_at: '2024-01-02T03:04:00Z', all_day: false })}
          now={new Date('2025-01-01T00:00:00Z')}
        />
      </IntlProvider>,
    );

    expect(screen.getByText(/2024/)).toBeInTheDocument();
  });
});
