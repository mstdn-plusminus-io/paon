import { useCallback, useEffect, useState } from 'react';

import { FormattedMessage } from 'react-intl';

import type {
  List as ImmutableList,
  Map as ImmutableMap,
  Record as ImmutableRecord,
} from 'immutable';

import type { History } from 'history';

import type { Account } from '../../types/resources';
import Card from '../features/status/components/card';
import { domain } from '../initial_state';

import { AbsoluteTimestamp } from './absolute_timestamp';
import { AnimatedNumber } from './animated_number';
import { Avatar } from './avatar';
import { ContentWarning } from './content_warning';
import { DisplayName } from './display_name';
import EditedTimestamp from './edited_timestamp';
import { Icon } from './icon';
import MediaAttachments from './media_attachments';
import { RelativeTimestamp } from './relative_timestamp';
import StatusContent from './status_content';

type Visibility = 'public' | 'unlisted' | 'private' | 'direct';

export type StatusQuote = ImmutableMap<
  'state' | 'quoted_status',
  string | null
>;

type Status = ImmutableRecord<{
  account: Account;
  card: ImmutableMap<string, unknown> | null;
  created_at: string;
  edited_at: string | null;
  favourites_count: number;
  hidden: boolean;
  id: string;
  language: string | null;
  media_attachments: ImmutableList<ImmutableMap<string, unknown>>;
  quote: StatusQuote | null;
  reblogs_count: number;
  sensitive: boolean;
  spoilerHtml: string;
  spoiler_text: string;
  url: string;
  visibility: Visibility;
}>;

interface QuoteErrorProps {
  accountId?: string | null;
  isLoading?: boolean;
  onRevealAccount?: (accountId: string) => void;
  state: string;
}

export const QuoteError: React.FC<QuoteErrorProps> = ({
  accountId,
  isLoading,
  onRevealAccount,
  state,
}) => {
  const handleRevealAccount = useCallback(() => {
    if (accountId) {
      onRevealAccount?.(accountId);
    }
  }, [accountId, onRevealAccount]);

  let message: React.ReactNode;

  if (isLoading) {
    message = (
      <FormattedMessage
        id='status.quote_loading'
        defaultMessage='Loading quoted post…'
      />
    );
  } else if (state === 'filtered') {
    message = (
      <FormattedMessage
        id='status.quote_error.filtered'
        defaultMessage='Hidden due to one of your filters'
      />
    );
  } else if (state === 'deleted' || state === 'soft_deleted') {
    message = (
      <FormattedMessage
        id='status.quote_error.removed'
        defaultMessage='This post was removed by its author.'
      />
    );
  } else if (state === 'unauthorized') {
    message = (
      <FormattedMessage
        id='status.quote_error.unauthorized'
        defaultMessage='This post cannot be displayed as you are not authorized to view it.'
      />
    );
  } else if (state === 'pending') {
    message = (
      <FormattedMessage
        id='status.quote_error.pending_approval'
        defaultMessage='This post is pending approval from the original author.'
      />
    );
  } else if (state === 'rejected' || state === 'revoked') {
    message = (
      <FormattedMessage
        id='status.quote_error.rejected'
        defaultMessage='This post cannot be displayed as the original author does not allow it to be quoted.'
      />
    );
  } else if (state === 'blocked_account') {
    message = (
      <FormattedMessage
        id='status.quote_error.blocked_account'
        defaultMessage='This quoted post is from an account you blocked.'
      />
    );
  } else if (state === 'blocked_domain') {
    message = (
      <FormattedMessage
        id='status.quote_error.blocked_domain'
        defaultMessage='This quoted post is from a domain you blocked.'
      />
    );
  } else if (state === 'muted_account') {
    message = (
      <FormattedMessage
        id='status.quote_error.muted_account'
        defaultMessage='This quoted post is from an account you muted.'
      />
    );
  } else if (state === 'limited' && accountId) {
    message = (
      <>
        <FormattedMessage
          id='status.quote_error.limited_account_hint.title'
          defaultMessage='This account has been hidden by the moderators of {domain}.'
          values={{ domain }}
        />{' '}
        <button
          className='link-button'
          onClick={handleRevealAccount}
          type='button'
        >
          <FormattedMessage
            id='status.quote_error.limited_account_hint.action'
            defaultMessage='Show anyway'
          />
        </button>
      </>
    );
  } else {
    message = (
      <FormattedMessage
        id='status.quote_error.not_found'
        defaultMessage='This post cannot be displayed.'
      />
    );
  }

  return (
    <div className='status__quote status__quote--error' role='status'>
      <Icon id='quote-right' fixedWidth className='status__quote__icon' />
      <div className='status__quote__inner'>{message}</div>
    </div>
  );
};

interface Props {
  history: History;
  id: string;
  nestingLevel?: number;
  onOpenMedia?: (
    statusId: string,
    media: unknown,
    index: number,
    lang: string | null,
  ) => void;
  onOpenVideo?: (
    statusId: string,
    media: unknown,
    lang: string | null,
    options: unknown,
  ) => void;
  renderNestedQuote?: (
    quote: StatusQuote,
    parentQuotePostId: string,
    nestingLevel: number,
  ) => React.ReactNode;
  status: Status;
  variant?: 'full' | 'link';
}

export const Quote: React.FC<Props> = ({
  history,
  id,
  nestingLevel = 1,
  onOpenMedia,
  onOpenVideo,
  renderNestedQuote,
  status,
  variant = 'full',
}) => {
  const spoilerText = status.get('spoiler_text');
  const initiallyExpanded = !status.get('hidden') || spoilerText.length === 0;
  const [expanded, setExpanded] = useState(initiallyExpanded);
  const language = status.get('language');

  useEffect(() => {
    setExpanded(initiallyExpanded);
  }, [id, initiallyExpanded]);

  const onClickStatus = useCallback<React.MouseEventHandler<HTMLAnchorElement>>(
    (event) => {
      if (event.button !== 0 || event.ctrlKey || event.metaKey) {
        return;
      }

      event.preventDefault();
      const account = status.get('account');
      history.push(`/@${account.get('acct')}/${status.get('id')}`);
    },
    [history, status],
  );

  const handleExpandedToggle = useCallback(() => {
    setExpanded((value) => !value);
  }, []);

  const handleOpenMedia = useCallback(
    (media: unknown, index: number) => {
      onOpenMedia?.(status.get('id'), media, index, language);
    },
    [language, onOpenMedia, status],
  );

  const handleOpenVideo = useCallback(
    (_mediaLanguage: string, options: unknown) => {
      onOpenVideo?.(
        status.get('id'),
        status.getIn(['media_attachments', 0]),
        language,
        options,
      );
    },
    [language, onOpenVideo, status],
  );

  const account = status.get('account');

  if (variant === 'link') {
    return (
      <a
        className='status__quote__nested-link'
        href={status.get('url')}
        onClick={onClickStatus}
      >
        <Icon id='quote-right' fixedWidth />
        <FormattedMessage
          id='status.quote_post_author'
          defaultMessage='Post by {name}'
          values={{
            name: account.get('display_name') || account.get('username'),
          }}
        />
      </a>
    );
  }

  let timestamp: React.ReactNode;
  if (localStorage.plusminus_config_timestamp === 'absolute') {
    timestamp = <AbsoluteTimestamp timestamp={status.get('created_at')} />;
  } else {
    timestamp = (
      <RelativeTimestamp
        timestamp={status.get('created_at')}
        year={new Date().getFullYear()}
      />
    );
  }

  const editedAt = status.get('edited_at');
  const edited = editedAt ? (
    <>
      {' · '}
      <EditedTimestamp statusId={status.get('id')} timestamp={editedAt} />
    </>
  ) : null;

  const visibilityIconInfo: Record<
    Visibility,
    { icon: string; text?: string }
  > = {
    public: { icon: 'globe' },
    unlisted: { icon: 'unlock' },
    private: { icon: 'lock' },
    direct: { icon: 'at' },
  };
  const visibilityIcon = visibilityIconInfo[status.get('visibility')];

  const quotedQuote = status.get('quote');
  const nestedQuote =
    expanded && quotedQuote && renderNestedQuote
      ? renderNestedQuote(quotedQuote, status.get('id'), nestingLevel + 1)
      : null;
  return (
    <article
      className='status__quote status__quote__container'
      data-nosnippet={
        (status.getIn(['account', 'noindex']) as boolean | undefined) !==
          false || undefined
      }
    >
      <Icon id='quote-right' fixedWidth className='status__quote__icon' />
      <div className='status__quote__inner'>
        <a
          className='status__quote__inner__header'
          href={status.get('url')}
          onClick={onClickStatus}
        >
          <Avatar size={32} account={account} />
          <DisplayName account={account} />
        </a>

        {spoilerText.length > 0 && (
          <ContentWarning
            text={status.get('spoilerHtml')}
            expanded={expanded}
            onClick={handleExpandedToggle}
          />
        )}

        {expanded && (
          <>
            <div className='status__quote__inner__content'>
              <StatusContent status={status} />
            </div>

            {status.get('media_attachments').size > 0 ? (
              <MediaAttachments
                status={status}
                onOpenMedia={handleOpenMedia}
                onOpenVideo={handleOpenVideo}
              />
            ) : status.get('card') && !quotedQuote ? (
              <Card
                card={status.get('card')}
                compact
                onOpenMedia={handleOpenMedia}
                sensitive={status.get('sensitive')}
              />
            ) : null}

            {nestedQuote}
          </>
        )}

        <div className='status__quote__inner__footer'>
          <a href={status.get('url')} onClick={onClickStatus}>
            {timestamp}
          </a>
          {edited}
          {' · '}
          <Icon id={visibilityIcon.icon} title={visibilityIcon.text} />
          {!['private', 'direct'].includes(status.get('visibility')) && (
            <>
              {' · '}
              <Icon id='retweet' />
              <span className='detailed-status__reblogs'>
                <AnimatedNumber value={status.get('reblogs_count')} />
              </span>
            </>
          )}
          {' · '}
          <Icon id='star' />
          <span className='detailed-status__favorites'>
            <AnimatedNumber value={status.get('favourites_count')} />
          </span>
        </div>
      </div>
    </article>
  );
};
