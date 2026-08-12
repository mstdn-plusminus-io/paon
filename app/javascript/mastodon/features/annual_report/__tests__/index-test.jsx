import { IntlProvider } from 'react-intl';

import { render, screen, waitFor } from '@testing-library/react';

import {
  importFetchedAccounts,
  importFetchedStatuses,
} from 'mastodon/actions/importer';
import { apiRequestGet, apiRequestPost } from 'mastodon/api';
import { useAppDispatch } from 'mastodon/store';

import { AnnualReport } from '..';

jest.mock('mastodon/api', () => ({
  apiRequestGet: jest.fn(),
  apiRequestPost: jest.fn(),
}));

jest.mock('mastodon/actions/importer', () => ({
  importFetchedAccounts: jest.fn((accounts) => ({
    type: 'TEST_ACCOUNTS_IMPORT',
    accounts,
  })),
  importFetchedStatuses: jest.fn((statuses) => ({
    type: 'TEST_STATUSES_IMPORT',
    statuses,
  })),
}));

jest.mock('mastodon/store', () => ({
  useAppDispatch: jest.fn(),
}));

jest.mock('mastodon/containers/status_container', () => ({
  __esModule: true,
  default: ({ id }) => <div data-testid='highlighted-status'>{id}</div>,
}));

describe('<AnnualReport />', () => {
  const dispatch = jest.fn();

  beforeEach(() => {
    jest.clearAllMocks();
    useAppDispatch.mockReturnValue(dispatch);
    apiRequestPost.mockResolvedValue(undefined);
  });

  it('imports referenced entities, renders the report and marks it read', async () => {
    const accounts = [{ id: '1' }];
    const statuses = [{ id: '123' }];

    apiRequestGet.mockResolvedValue({
      annual_reports: [
        {
          year: 2025,
          schema_version: 1,
          data: {
            archetype: 'oracle',
            time_series: [
              { month: 1, statuses: 12, following: 2, followers: 4 },
            ],
            top_hashtags: [{ name: 'paon', count: 5 }],
            most_used_apps: [{ name: 'Web', count: 12 }],
            top_statuses: { by_reblogs: 123 },
            percentiles: { statuses: 80, followers: 50 },
          },
        },
      ],
      accounts,
      statuses,
    });

    render(
      <IntlProvider locale='en'>
        <AnnualReport year='2025' />
      </IntlProvider>,
    );

    expect(await screen.findByText('#paon')).toBeInTheDocument();
    expect(screen.getByText('The oracle')).toBeInTheDocument();
    expect(screen.getByTestId('highlighted-status')).toHaveTextContent('123');
    expect(apiRequestGet).toHaveBeenCalledWith('v1/annual_reports/2025');
    expect(importFetchedAccounts).toHaveBeenCalledWith(accounts);
    expect(importFetchedStatuses).toHaveBeenCalledWith(statuses);
    expect(dispatch).toHaveBeenCalledTimes(2);

    await waitFor(() => {
      expect(apiRequestPost).toHaveBeenCalledWith(
        'v1/annual_reports/2025/read',
      );
    });
  });
});
