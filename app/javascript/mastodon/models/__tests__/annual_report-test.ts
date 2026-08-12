import type { AnnualReport } from '../annual_report';
import { summarizeAnnualReport } from '../annual_report';

describe('summarizeAnnualReport', () => {
  it('normalizes the v1 report data used by the Paon annual report view', () => {
    const report: AnnualReport = {
      year: 2025,
      schema_version: 1,
      data: {
        archetype: 'replier',
        time_series: [
          { month: 1, statuses: 10, following: 1, followers: 3 },
          { month: 2, statuses: 5, following: 0, followers: 2 },
        ],
        top_hashtags: [{ name: 'paon', count: 8 }],
        most_used_apps: [{ name: 'Web', count: 12 }],
        top_statuses: {
          by_reblogs: 123,
          by_favourites: 456,
          by_replies: 789,
        },
        percentiles: { followers: 70, statuses: 99.8 },
      },
    };

    expect(summarizeAnnualReport(report)).toEqual({
      archetype: 'replier',
      followers: 5,
      highlightedStatusId: '123',
      highlightedStatusKind: 'by_reblogs',
      mostUsedApp: { name: 'Web', count: 12 },
      mostUsedHashtag: { name: 'paon', count: 8 },
      posts: 15,
      statusPercentile: 99,
    });
  });

  it('handles a valid report whose optional v1 collections are empty', () => {
    expect(
      summarizeAnnualReport({
        year: 2025,
        schema_version: 1,
        data: {},
      }),
    ).toMatchObject({
      followers: 0,
      posts: 0,
      statusPercentile: 0,
      highlightedStatusId: undefined,
    });
  });
});
