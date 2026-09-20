import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { FormattedMessage } from 'react-intl';

import classNames from 'classnames';
import { Link } from 'react-router-dom';

import { Blurhash } from 'mastodon/components/blurhash';
import { RelativeTimestamp } from 'mastodon/components/relative_timestamp';
import { ShortNumber } from 'mastodon/components/short_number';
import { Skeleton } from 'mastodon/components/skeleton';

import { AuthorLink } from './author_link';

const sharesCountRenderer = (displayNumber, pluralReady) => (
  <FormattedMessage
    id='link_preview.shares'
    defaultMessage='{count, plural, one {{counter} post} other {{counter} posts}}'
    values={{
      count: pluralReady,
      counter: <strong>{displayNumber}</strong>,
    }}
  />
);

export default class Story extends PureComponent {

  static propTypes = {
    url: PropTypes.string,
    title: PropTypes.string,
    lang: PropTypes.string,
    publisher: PropTypes.string,
    publishedAt: PropTypes.string,
    author: PropTypes.string,
    authorUrl: PropTypes.string,
    authorAccount: PropTypes.string,
    sharedTimes: PropTypes.number,
    thumbnail: PropTypes.string,
    thumbnailDescription: PropTypes.string,
    blurhash: PropTypes.string,
    expanded: PropTypes.bool,
  };

  state = {
    thumbnailLoaded: false,
  };

  handleImageLoad = () => this.setState({ thumbnailLoaded: true });

  render () {
    const { expanded, url, title, lang, publisher, author, authorUrl, authorAccount, publishedAt, sharedTimes, thumbnail, thumbnailDescription, blurhash } = this.props;

    const { thumbnailLoaded } = this.state;

    return (
      <div className={classNames('story', { expanded })}>
        <div className='story__details'>
          <div className='story__details__publisher'>{publisher ? <span lang={lang}>{publisher}</span> : <Skeleton width={50} />}{publishedAt && <> · <RelativeTimestamp timestamp={publishedAt} /></>}</div>
          <a className='story__details__title' lang={lang} href={url} target='_blank' rel='noopener noreferrer'>{title ? title : <Skeleton />}</a>
          <div className='story__details__shared'>
            {author ? <FormattedMessage id='link_preview.author' defaultMessage='By {name}' values={{ name: authorAccount ? <AuthorLink accountId={authorAccount} /> : authorUrl ? <a href={authorUrl} target='_blank' rel='noopener noreferrer'><strong>{author}</strong></a> : <strong>{author}</strong> }} /> : <span />}
            {typeof sharedTimes === 'number' && url ? <Link className='story__details__shared__pill' to={`/links/${encodeURIComponent(url)}`}><ShortNumber value={sharedTimes} renderer={sharesCountRenderer} /></Link> : <Skeleton width={100} />}
          </div>
        </div>

        <a className='story__thumbnail' href={url} target='_blank' rel='noopener noreferrer'>
          {thumbnail ? (
            <>
              <div className={classNames('story__thumbnail__preview', { 'story__thumbnail__preview--hidden': thumbnailLoaded })}><Blurhash hash={blurhash} /></div>
              <img src={thumbnail} onLoad={this.handleImageLoad} alt={thumbnailDescription} title={thumbnailDescription} lang={lang} />
            </>
          ) : <Skeleton />}
        </a>
      </div>
    );
  }

}
