export interface AnnualReportNameAndCount {
  name: string;
  count: number;
}

export interface AnnualReportTimeSeriesMonth {
  month: number;
  statuses: number;
  following: number;
  followers: number;
}

export interface AnnualReportTopStatuses {
  by_reblogs?: number | string | null;
  by_favourites?: number | string | null;
  by_replies?: number | string | null;
}

export type AnnualReportArchetype =
  | 'lurker'
  | 'booster'
  | 'pollster'
  | 'replier'
  | 'oracle';

export interface AnnualReportV1Data {
  most_used_apps?: AnnualReportNameAndCount[];
  percentiles?: {
    followers?: number;
    statuses?: number;
  };
  top_hashtags?: AnnualReportNameAndCount[];
  top_statuses?: AnnualReportTopStatuses;
  time_series?: AnnualReportTimeSeriesMonth[];
  archetype?: AnnualReportArchetype;
}

export interface AnnualReport {
  year: number;
  schema_version: number;
  data: AnnualReportV1Data;
}

export interface AnnualReportSummary {
  archetype?: AnnualReportArchetype;
  followers: number;
  highlightedStatusId?: string;
  highlightedStatusKind?: keyof AnnualReportTopStatuses;
  mostUsedApp?: AnnualReportNameAndCount;
  mostUsedHashtag?: AnnualReportNameAndCount;
  posts: number;
  statusPercentile: number;
}

export const summarizeAnnualReport = (
  report: AnnualReport,
): AnnualReportSummary => {
  const timeSeries = Array.isArray(report.data.time_series)
    ? report.data.time_series
    : [];
  const topStatuses = report.data.top_statuses ?? {};
  const highlightedStatusKind = (
    ['by_reblogs', 'by_favourites', 'by_replies'] as const
  ).find((kind) => {
    const value = topStatuses[kind];
    return value !== null && value !== undefined && String(value) !== '0';
  });
  const highlightedStatusValue = highlightedStatusKind
    ? topStatuses[highlightedStatusKind]
    : undefined;

  return {
    archetype: report.data.archetype,
    followers: timeSeries.reduce(
      (total, month) => total + (Number(month.followers) || 0),
      0,
    ),
    highlightedStatusId:
      highlightedStatusValue === null || highlightedStatusValue === undefined
        ? undefined
        : String(highlightedStatusValue),
    highlightedStatusKind,
    mostUsedApp: report.data.most_used_apps?.[0],
    mostUsedHashtag: report.data.top_hashtags?.[0],
    posts: timeSeries.reduce(
      (total, month) => total + (Number(month.statuses) || 0),
      0,
    ),
    statusPercentile: Math.min(
      99,
      Math.max(0, Number(report.data.percentiles?.statuses) || 0),
    ),
  };
};
