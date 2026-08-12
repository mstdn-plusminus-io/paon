import { IntlProvider } from 'react-intl';

import { render, screen } from '@testing-library/react';

import { FormattedDateWrapper } from '../formatted_date';

describe('Mastodon 4.4 semantic timestamps', () => {
  it('renders a machine-readable ISO datetime without changing displayed formatting', () => {
    render(
      <IntlProvider locale='en' timeZone='UTC'>
        <FormattedDateWrapper
          value='2025-01-02T03:04:05Z'
          year='numeric'
          month='short'
          day='2-digit'
        />
      </IntlProvider>,
    );

    const timestamp = screen.getByText('Jan 02, 2025');
    expect(timestamp.tagName).toBe('TIME');
    expect(timestamp).toHaveAttribute('datetime', '2025-01-02T03:04:05.000Z');
  });
});
