import { useCallback, useEffect, useMemo, useState } from 'react';

import { FormattedMessage, FormattedNumber, useIntl } from 'react-intl';

import {
  importFetchedAccounts,
  importFetchedStatuses,
} from 'mastodon/actions/importer';
import { apiRequestGet, apiRequestPost } from 'mastodon/api';
import type {
  ApiAccountJSON,
  ApiStatusJSON,
} from 'mastodon/api_types/notifications';
import Button from 'mastodon/components/button';
import { LoadingIndicator } from 'mastodon/components/loading_indicator';
import StatusContainer from 'mastodon/containers/status_container';
import type {
  AnnualReport as AnnualReportData,
  AnnualReportArchetype,
  AnnualReportTopStatuses,
} from 'mastodon/models/annual_report';
import { summarizeAnnualReport } from 'mastodon/models/annual_report';
import { useAppDispatch } from 'mastodon/store';

interface AnnualReportResponse {
  annual_reports: AnnualReportData[];
  accounts: ApiAccountJSON[];
  statuses: ApiStatusJSON[];
}

const ArchetypeLabel: React.FC<{ archetype?: AnnualReportArchetype }> = ({
  archetype,
}) => {
  switch (archetype) {
    case 'booster':
      return (
        <FormattedMessage
          id='annual_report.summary.archetype.booster'
          defaultMessage='The cool-hunter'
        />
      );
    case 'replier':
      return (
        <FormattedMessage
          id='annual_report.summary.archetype.replier'
          defaultMessage='The social butterfly'
        />
      );
    case 'pollster':
      return (
        <FormattedMessage
          id='annual_report.summary.archetype.pollster'
          defaultMessage='The pollster'
        />
      );
    case 'oracle':
      return (
        <FormattedMessage
          id='annual_report.summary.archetype.oracle'
          defaultMessage='The oracle'
        />
      );
    case 'lurker':
    default:
      return (
        <FormattedMessage
          id='annual_report.summary.archetype.lurker'
          defaultMessage='The lurker'
        />
      );
  }
};

const HighlightLabel: React.FC<{
  kind?: keyof AnnualReportTopStatuses;
}> = ({ kind }) => {
  switch (kind) {
    case 'by_favourites':
      return (
        <FormattedMessage
          id='annual_report.summary.highlighted_post.by_favourites'
          defaultMessage='Most favourited post'
        />
      );
    case 'by_replies':
      return (
        <FormattedMessage
          id='annual_report.summary.highlighted_post.by_replies'
          defaultMessage='Post with the most replies'
        />
      );
    case 'by_reblogs':
    default:
      return (
        <FormattedMessage
          id='annual_report.summary.highlighted_post.by_reblogs'
          defaultMessage='Most boosted post'
        />
      );
  }
};

export const AnnualReport: React.FC<{ year: string }> = ({ year }) => {
  const dispatch = useAppDispatch();
  const intl = useIntl();
  const [response, setResponse] = useState<AnnualReportResponse>();
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let active = true;

    setLoading(true);
    setFailed(false);

    const loadReport = async () => {
      try {
        const data = await apiRequestGet<AnnualReportResponse>(
          `v1/annual_reports/${year}`,
        );
        if (!active) return;

        if (data.accounts.length > 0) {
          dispatch(importFetchedAccounts(data.accounts));
        }
        if (data.statuses.length > 0) {
          dispatch(importFetchedStatuses(data.statuses));
        }

        setResponse(data);
        setLoading(false);

        // Reading the report must not hide an otherwise successfully loaded
        // report if marking it as read fails temporarily.
        try {
          await apiRequestPost(`v1/annual_reports/${year}/read`);
        } catch {
          // The notification can be dismissed or marked read on the next view.
        }
      } catch {
        if (!active) return;
        setResponse(undefined);
        setLoading(false);
        setFailed(true);
      }
    };

    void loadReport();

    return () => {
      active = false;
    };
  }, [dispatch, reload, year]);

  const report = response?.annual_reports[0];
  const summary = useMemo(
    () => (report ? summarizeAnnualReport(report) : undefined),
    [report],
  );
  const handleRetry = useCallback(() => {
    setReload((value) => value + 1);
  }, []);

  if (loading) {
    return (
      <div className='annual-report__loading'>
        <LoadingIndicator />
      </div>
    );
  }

  if (failed) {
    return (
      <div className='annual-report__empty' role='alert'>
        <p>
          <FormattedMessage
            id='annual_report.error'
            defaultMessage='Your annual report could not be loaded.'
          />
        </p>
        <Button
          text={intl.formatMessage({
            id: 'annual_report.retry',
            defaultMessage: 'Try again',
          })}
          onClick={handleRetry}
        />
      </div>
    );
  }

  if (!report || !summary) {
    return (
      <div className='annual-report__empty'>
        <FormattedMessage
          id='annual_report.unavailable'
          defaultMessage='This annual report is no longer available.'
        />
      </div>
    );
  }

  return (
    <div className='annual-report'>
      <header className='annual-report__intro'>
        <h2>
          <FormattedMessage
            id='annual_report.summary.thanks'
            defaultMessage='Thanks for being part of Mastodon!'
          />
        </h2>
        <p>
          <FormattedMessage
            id='annual_report.summary.here_it_is'
            defaultMessage='Here is your {year} in review:'
            values={{ year: report.year }}
          />
        </p>
      </header>

      <dl className='annual-report__stats'>
        <div className='annual-report__stat annual-report__stat--wide'>
          <dt>
            <FormattedMessage
              id='annual_report.summary.archetype.label'
              defaultMessage='Your Mastodon archetype'
            />
          </dt>
          <dd>
            <ArchetypeLabel archetype={summary.archetype} />
          </dd>
        </div>
        <div className='annual-report__stat'>
          <dt>
            <FormattedMessage
              id='annual_report.summary.new_posts.new_posts'
              defaultMessage='New posts'
            />
          </dt>
          <dd>
            <FormattedNumber value={summary.posts} />
          </dd>
        </div>
        <div className='annual-report__stat'>
          <dt>
            <FormattedMessage
              id='annual_report.summary.followers.followers'
              defaultMessage='Followers'
            />
          </dt>
          <dd>
            {summary.followers >= 0 && '+'}
            <FormattedNumber value={summary.followers} />
          </dd>
        </div>
        <div className='annual-report__stat'>
          <dt>
            <FormattedMessage
              id='annual_report.summary.most_used_hashtag.most_used_hashtag'
              defaultMessage='Most used hashtag'
            />
          </dt>
          <dd>
            {summary.mostUsedHashtag ? (
              `#${summary.mostUsedHashtag.name}`
            ) : (
              <FormattedMessage
                id='annual_report.summary.most_used_hashtag.none'
                defaultMessage='None'
              />
            )}
          </dd>
        </div>
        <div className='annual-report__stat'>
          <dt>
            <FormattedMessage
              id='annual_report.summary.most_used_app.most_used_app'
              defaultMessage='Most used app'
            />
          </dt>
          <dd>{summary.mostUsedApp?.name ?? '—'}</dd>
        </div>
        <div className='annual-report__stat annual-report__stat--wide'>
          <dt>
            <FormattedMessage
              id='annual_report.summary.percentile.label'
              defaultMessage='Posting percentile on this server'
            />
          </dt>
          <dd>
            <FormattedNumber
              value={summary.statusPercentile / 100}
              style='percent'
              maximumFractionDigits={summary.statusPercentile < 1 ? 1 : 0}
            />
          </dd>
        </div>
      </dl>

      {summary.highlightedStatusId && (
        <section className='annual-report__highlight'>
          <h3>
            <HighlightLabel kind={summary.highlightedStatusKind} />
          </h3>
          <StatusContainer
            id={summary.highlightedStatusId}
            contextType='annual-report'
            muted
          />
        </section>
      )}
    </div>
  );
};
