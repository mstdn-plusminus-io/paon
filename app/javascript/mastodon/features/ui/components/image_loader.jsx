import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import classNames from 'classnames';

import { LoadingBar } from 'react-redux-loading-bar';
import { TransformWrapper, TransformComponent } from 'react-zoom-pan-pinch';

import { IconButton } from '../../../components/icon_button';

export default class ImageLoader extends PureComponent {

  static propTypes = {
    alt: PropTypes.string,
    lang: PropTypes.string,
    src: PropTypes.string.isRequired,
    onClick: PropTypes.func,
    onZoomChange: PropTypes.func,
    navigationHidden: PropTypes.bool,
  }

  static defaultProps = {
    alt: '',
    lang: '',
    width: null,
    height: null,
  };

  state = {
    loading: true,
    error: false,
    width: null,
    minScale: 0,
    visibility: 'hidden',
    currentScale: 0,
  }

  removers = [];

  img = null;
  transformState = null;
  centerView = null;
  zoomToElement = null;

  componentDidMount() {
    window.addEventListener('resize', this.onResize);
    this.removers.push(() => window.removeEventListener('resize', this.onResize));
  }

  componentWillUnmount () {
    this.removeEventListeners();
  }

  removeEventListeners () {
    this.removers.forEach(listeners => listeners());
    this.removers = [];
  }

  onClickImage = onClick => e => {
    e.stopPropagation();
    e.preventDefault();

    onClick();
  }

  onLoadImage = e => {
    this.img = e.currentTarget;

    this.setState({
      loading: false,
    });
    if (this.zoomToElement && (this.img.naturalWidth > window.innerWidth || this.img.naturalHeight > window.innerHeight)) {
      this.zoomToElement(e.currentTarget, undefined, 0);
    }
    if (this.centerView) {
      this.centerView(undefined, 0);
    }
    setTimeout(() => {
      if (this.state.minScale === 0) {
        const minScale = this.transformState.scale < 1.0 ? this.transformState.scale : 1.0;
        this.setState({
          minScale,
          currentScale: minScale,
          visibility: 'visible',
        });
      }
    }, 0);
  }

  lastResized = null;

  onResize = () => {
    if (this.lastResized) {
      clearTimeout(this.lastResized);
    }
    this.lastResized = setTimeout(() => {
      this.setState({
        minScale: 0,
      }, () => {
        if (this.zoomToElement) {
          this.zoomToElement(this.img, undefined, 0);
          setTimeout(() => {
            const minScale = this.transformState.scale < 1.0 ? this.transformState.scale : 1.0;
            this.setState({
              minScale,
              currentScale: minScale,
            });
          }, 0);
        }
      });
    }, 32);
  }

  onZoom = (ref) => {
    if (ref.state.scale < this.state.minScale) {
      this.setState({
        currentScale: this.state.minScale,
      });
      this.props.onZoomChange?.(false);
      return;
    }

    this.setState({
      currentScale: ref.state.scale,
    });
    this.props.onZoomChange?.(ref.state.scale > this.state.minScale + 0.01);
  }

  onClickZoomButton = zoomButtonState => e => {
    e.stopPropagation();

    if (zoomButtonState === 'compress') {
      if (this.zoomToElement) {
        this.zoomToElement(this.img, undefined, 0);
      }
    } else {
      if (this.centerView) {
        this.centerView(1.0, 0);
      }
    }
    setTimeout(() =>
      this.setState({
        currentScale: this.transformState.scale,
      }, () => this.props.onZoomChange?.(this.state.currentScale > this.state.minScale + 0.01)), 0);
  }

  render () {
    const { alt, lang, src, onClick } = this.props;
    const { loading } = this.state;

    const className = classNames('image-loader', {
      'image-loader--loading': loading,
    });

    const zoomButtonState = this.state.currentScale >= 1.0 ? 'compress' : 'expand';

    let maxScale = parseInt(localStorage.plusminus_config_max_image_scale);
    if (isNaN(maxScale)) {
      maxScale = 400;
    }
    maxScale /= 100;

    return (
      <div className={className}>
        {loading && (
          <div className='loading-bar__container'>
            <LoadingBar className='loading-bar' loading={1} />
          </div>
        )}
        <TransformWrapper
          minScale={this.state.minScale}
          maxScale={maxScale}
          initialScale={1}
          limitToBounds
          centerZoomedOut
          centerOnInit
          onZoom={this.onZoom}
          onZoomStop={this.onZoom}
        >
          {({ state, zoomToElement, centerView }) => {
            this.transformState = state;
            this.centerView = centerView;
            this.zoomToElement = zoomToElement;
            return (
              <TransformComponent
                wrapperStyle={{
                  width: '100dvw',
                  height: '100dvh',
                  overflow: 'hidden',
                }}
                contentStyle={{
                  width: 'fit-content',
                  height: 'fit-content',
                  transformOrigin: 'left top',
                }}
              >
                {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions -- image click is intentional */}
                <img src={src} alt={alt} lang={lang} onLoad={this.onLoadImage} style={{ visibility: this.state.visibility }} onClick={this.onClickImage(onClick)} />
              </TransformComponent>
            );
          }}
        </TransformWrapper>
        {!loading && (
          <div
            className={classNames('media-modal__navigation', { 'media-modal__navigation--hidden': this.props.navigationHidden })}
          >
            <div style={{ margin: '16px 0 0 16px' }}>
              <div style={{ display: 'flex', alignItems: 'center' }}>
                <div>
                  <IconButton
                    className='media-modal__zoom-button'
                    title={zoomButtonState}
                    icon={zoomButtonState}
                    disabled={this.state.minScale === 1.0 && this.state.currentScale === 1.0}
                    onClick={this.onClickZoomButton(zoomButtonState)}
                    size={40}
                  />
                </div>
                <div className='media-modal__navigation-size' style={{ opacity: 0.8 }}>
                  <div>
                    {this.img.naturalWidth} x {this.img.naturalHeight} px
                  </div>
                  <div>
                    {Math.round(this.state.currentScale * 100)}%
                  </div>
                </div>
              </div>
            </div>
          </div>
        )}
      </div>
    );
  }

}
