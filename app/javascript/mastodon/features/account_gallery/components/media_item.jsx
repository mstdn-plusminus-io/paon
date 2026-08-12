import PropTypes from 'prop-types';

import classNames from 'classnames';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';

import HeadphonesIcon from '@/material-icons/400-24px/headphones-fill.svg?react';
import MovieIcon from '@/material-icons/400-24px/movie-fill.svg?react';
import VisibilityOffIcon from '@/material-icons/400-24px/visibility_off.svg?react';
import { AltTextBadge } from 'mastodon/components/alt_text_badge';
import { Blurhash } from 'mastodon/components/blurhash';
import { Icon }  from 'mastodon/components/icon';
import { formatTime } from 'mastodon/features/video';
import { autoPlayGif, displayMedia, useBlurhash } from 'mastodon/initial_state';

export default class MediaItem extends ImmutablePureComponent {

  static propTypes = {
    attachment: ImmutablePropTypes.map.isRequired,
    onOpenMedia: PropTypes.func.isRequired,
  };

  state = {
    visible: displayMedia !== 'hide_all' && !this.props.attachment.getIn(['status', 'sensitive']) || displayMedia === 'show_all',
    loaded: false,
    error: false,
  };

  handleImageLoad = () => {
    this.setState({ loaded: true });
  };

  handleImageError = () => {
    this.setState({ error: true });
  };

  handleMouseEnter = e => {
    if (this.hoverToPlay()) {
      e.target.play();
    }
  };

  handleMouseLeave = e => {
    if (this.hoverToPlay()) {
      e.target.pause();
      e.target.currentTime = 0;
    }
  };

  hoverToPlay () {
    return !autoPlayGif && ['gifv', 'video'].indexOf(this.props.attachment.get('type')) !== -1;
  }

  handleClick = e => {
    if (e.button === 0 && !(e.ctrlKey || e.metaKey)) {
      e.preventDefault();

      if (this.state.visible) {
        this.props.onOpenMedia(this.props.attachment);
      } else {
        this.setState({ visible: true });
      }
    }
  };

  render () {
    const { attachment } = this.props;
    const { visible, loaded } = this.state;

    const status = attachment.get('status');
    const description = attachment.getIn(['translation', 'description']) || attachment.get('description');
    const title  = status.get('spoiler_text') || description;
    const type = attachment.get('type');

    let thumbnail;
    const badges = [];

    if (description) {
      badges.push(<AltTextBadge key='alt' description={description} />);
    }

    if (!visible) {
      thumbnail = (
        <div className='media-gallery__item__overlay'>
          <Icon id='eye-slash' icon={VisibilityOffIcon} />
        </div>
      );
    } else if (type === 'audio') {
      thumbnail = (
        <>
          <img
            src={attachment.get('preview_url') || attachment.get('preview_remote_url') || status.getIn(['account', 'avatar_static'])}
            alt={description}
            title={description}
            lang={status.get('language')}
            onLoad={this.handleImageLoad}
            onError={this.handleImageError}
          />

          <div className='media-gallery__item__overlay media-gallery__item__overlay--corner'>
            <Icon id='music' icon={HeadphonesIcon} />
          </div>
        </>
      );
    } else if (type === 'image') {
      const focusX = attachment.getIn(['meta', 'focus', 'x']) || 0;
      const focusY = attachment.getIn(['meta', 'focus', 'y']) || 0;
      const x      = ((focusX / 2) + .5) * 100;
      const y      = ((focusY / -2) + .5) * 100;

      thumbnail = (
        <img
          src={attachment.get('preview_url') || attachment.get('preview_remote_url')}
          alt={description}
          title={description}
          lang={status.get('language')}
          style={{ objectPosition: `${x}% ${y}%` }}
          onLoad={this.handleImageLoad}
          onError={this.handleImageError}
        />
      );
    } else if (['video', 'gifv'].includes(type)) {
      const duration = attachment.getIn(['meta', 'original', 'duration']);

      thumbnail = (
        <div className='media-gallery__gifv'>
          <video
            className='media-gallery__item-gifv-thumbnail'
            aria-label={description}
            title={description}
            lang={status.get('language')}
            src={attachment.get('url') || attachment.get('remote_url')}
            onMouseEnter={this.handleMouseEnter}
            onMouseLeave={this.handleMouseLeave}
            onLoadedData={this.handleImageLoad}
            autoPlay={autoPlayGif}
            playsInline
            loop
            muted
          />

          {type === 'video' && (
            <div className='media-gallery__item__overlay media-gallery__item__overlay--corner'>
              <Icon id='play' icon={MovieIcon} />
            </div>
          )}
        </div>
      );

      badges.push(
        <span key={type} className='media-gallery__alt__label media-gallery__alt__label--non-interactive'>
          {type === 'gifv' ? 'GIF' : formatTime(Math.floor(duration || 0))}
        </span>,
      );
    }

    return (
      <div className={classNames('media-gallery__item media-gallery__item--square', { 'media-gallery__item--error': this.state.error })}>
        <Blurhash
          hash={attachment.get('blurhash')}
          className={classNames('media-gallery__preview', { 'media-gallery__preview--hidden': visible && loaded })}
          dummy={!useBlurhash}
        />

        <a className='media-gallery__item-thumbnail' href={`/@${status.getIn(['account', 'acct'])}/${status.get('id')}`} onClick={this.handleClick} title={title} target='_blank' rel='noopener noreferrer'>
          {thumbnail}
        </a>

        {badges.length > 0 && (
          <div className='media-gallery__item__badges'>{badges}</div>
        )}
      </div>
    );
  }

}
