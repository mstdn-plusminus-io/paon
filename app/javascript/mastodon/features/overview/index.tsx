import { useCallback, useEffect, useState } from 'react';

import { FormattedMessage, useIntl } from 'react-intl';

import { importFetchedStatuses } from 'mastodon/actions/importer';
import { fetchExtendedDescription, fetchServer } from 'mastodon/actions/server';
import { apiRequestGet } from 'mastodon/api';
import type { ApiStatusJSON } from 'mastodon/api_types/notifications';
import { ServerHeroImage } from 'mastodon/components/server_hero_image';
import StatusContainer from 'mastodon/containers/status_container';
import Column from 'mastodon/features/ui/components/column';
import { domain } from 'mastodon/initial_state';
import { useAppDispatch, useAppSelector } from 'mastodon/store';

type OverviewSection = 'activity' | 'about';

interface ServerOverviewInfo {
  isLoading?: boolean;
  domain?: string;
  title?: string;
  description?: string;
  thumbnail?: { url?: string; blurhash?: string };
  contact?: {
    email?: string;
    account?: {
      acct?: string;
      display_name?: string;
      url?: string;
    };
  };
}

interface ExtendedDescriptionInfo {
  isLoading?: boolean;
  content?: string;
}

interface ImmutableValue<Value> {
  toJS: () => Value;
}

const firstNonBlank = (...values: (string | undefined)[]) =>
  values.find((value) => value?.trim()) ?? '';

export const Overview: React.FC = () => {
  const intl = useIntl();
  const dispatch = useAppDispatch();
  const serverValue = useAppSelector((state) =>
    state.getIn(['server', 'server']),
  ) as Partial<ImmutableValue<ServerOverviewInfo>>;
  const extendedDescriptionValue = useAppSelector((state) =>
    state.getIn(['server', 'extendedDescription']),
  ) as Partial<ImmutableValue<ExtendedDescriptionInfo>>;
  const server = serverValue.toJS?.() ?? {};
  const extendedDescription = extendedDescriptionValue.toJS?.() ?? {};
  const [section, setSection] = useState<OverviewSection>('activity');
  const [statusIds, setStatusIds] = useState<string[]>([]);
  const [timelineLoading, setTimelineLoading] = useState(true);
  const [timelineFailed, setTimelineFailed] = useState(false);

  useEffect(() => {
    dispatch(fetchServer());
    dispatch(fetchExtendedDescription());

    let active = true;
    setTimelineLoading(true);
    setTimelineFailed(false);
    apiRequestGet<ApiStatusJSON[]>('v1/timelines/public', {
      local: true,
      limit: 40,
    })
      .then((statuses) => {
        if (!active) return undefined;
        dispatch(importFetchedStatuses(statuses));
        setStatusIds(statuses.map((status) => status.id));
        return undefined;
      })
      .catch(() => {
        if (active) {
          setStatusIds([]);
          setTimelineFailed(true);
        }
        return undefined;
      })
      .finally(() => {
        if (active) setTimelineLoading(false);
      });
    return () => {
      active = false;
    };
  }, [dispatch]);

  const showActivity = useCallback(() => {
    setSection('activity');
  }, []);
  const showAbout = useCallback(() => {
    setSection('about');
  }, []);

  const isServerLoading = Boolean(server.isLoading);
  const serverDomain = firstNonBlank(server.domain, domain);
  const serverTitle = firstNonBlank(server.title, serverDomain);
  const serverDescription = server.description ?? '';
  const thumbnailURL = server.thumbnail?.url ?? '';
  const thumbnailBlurhash = server.thumbnail?.blurhash ?? '';
  const contactAccount = server.contact?.account;
  const contactName = firstNonBlank(
    contactAccount?.display_name,
    contactAccount?.acct,
  );
  const contactAcct = contactAccount?.acct ?? '';
  const contactURL = contactAccount?.url ?? '';
  const contactEmail = server.contact?.email ?? '';
  const extendedContent = extendedDescription.content ?? '';
  const extendedLoading = Boolean(extendedDescription.isLoading);

  return (
    <Column
      heading={intl.formatMessage({
        id: 'overview.title',
        defaultMessage: 'Overview',
      })}
    >
      <div className='paon-feature-page'>
        {thumbnailURL && (
          <ServerHeroImage
            src={thumbnailURL}
            blurhash={thumbnailBlurhash}
            className='overview__hero'
          />
        )}
        <header className='overview__header'>
          <h1>{serverTitle}</h1>
          <p>{serverDescription}</p>
        </header>

        <div className='overview__tabs' role='tablist'>
          <button
            type='button'
            role='tab'
            aria-selected={section === 'activity'}
            className={section === 'activity' ? 'active' : undefined}
            onClick={showActivity}
          >
            <FormattedMessage
              id='overview.latest_activity'
              defaultMessage='Latest activity'
            />
          </button>
          <button
            type='button'
            role='tab'
            aria-selected={section === 'about'}
            className={section === 'about' ? 'active' : undefined}
            onClick={showAbout}
          >
            <FormattedMessage
              id='overview.about'
              defaultMessage='About this server'
            />
          </button>
        </div>

        {section === 'activity' ? (
          <section className='paon-feature-page__section' role='tabpanel'>
            <h2>
              <FormattedMessage
                id='overview.latest_posts'
                defaultMessage='Latest posts'
              />
            </h2>
            <p>
              <FormattedMessage
                id='overview.latest_activity_description'
                defaultMessage='These are the latest 40 public posts from accounts on this server.'
              />
            </p>
            {timelineLoading && (
              <p role='status'>
                <FormattedMessage
                  id='overview.loading'
                  defaultMessage='Loading latest posts…'
                />
              </p>
            )}
            {timelineFailed && (
              <p role='alert'>
                <FormattedMessage
                  id='overview.timeline_error'
                  defaultMessage='The latest posts could not be loaded.'
                />
              </p>
            )}
            {!timelineLoading && !timelineFailed && statusIds.length === 0 && (
              <p>
                <FormattedMessage
                  id='overview.timeline_empty'
                  defaultMessage='There are no public posts to show yet.'
                />
              </p>
            )}
          </section>
        ) : (
          <section className='paon-feature-page__section' role='tabpanel'>
            <h2>
              <FormattedMessage
                id='overview.about'
                defaultMessage='About this server'
              />
            </h2>
            {isServerLoading ? (
              <p role='status'>
                <FormattedMessage
                  id='overview.loading_server'
                  defaultMessage='Loading server information…'
                />
              </p>
            ) : (
              <div className='overview__metadata'>
                <div>
                  <h3>
                    <FormattedMessage
                      id='overview.administered_by'
                      defaultMessage='Administered by'
                    />
                  </h3>
                  {contactName ? (
                    <a href={contactURL ? contactURL : `/@${contactAcct}`}>
                      {contactName}
                      {contactAcct && <small>@{contactAcct}</small>}
                    </a>
                  ) : (
                    <FormattedMessage
                      id='overview.not_available'
                      defaultMessage='Not available'
                    />
                  )}
                </div>
                <div>
                  <h3>
                    <FormattedMessage
                      id='overview.contact'
                      defaultMessage='Contact'
                    />
                  </h3>
                  {contactEmail ? (
                    <a href={`mailto:${contactEmail}`}>{contactEmail}</a>
                  ) : (
                    <FormattedMessage
                      id='overview.not_available'
                      defaultMessage='Not available'
                    />
                  )}
                </div>
              </div>
            )}
            <div className='overview__description'>
              <h3>{serverDomain}</h3>
              {extendedLoading ? (
                <p role='status'>
                  <FormattedMessage
                    id='overview.loading_about'
                    defaultMessage='Loading server description…'
                  />
                </p>
              ) : extendedContent ? (
                <div
                  className='prose'
                  dangerouslySetInnerHTML={{ __html: extendedContent }}
                />
              ) : (
                <p>
                  <FormattedMessage
                    id='overview.about_empty'
                    defaultMessage='This server has not provided additional information.'
                  />
                </p>
              )}
            </div>
          </section>
        )}
      </div>

      {section === 'activity' &&
        statusIds.map((id) => (
          <StatusContainer key={id} id={id} contextType='public' />
        ))}
    </Column>
  );
};

export default Overview;
