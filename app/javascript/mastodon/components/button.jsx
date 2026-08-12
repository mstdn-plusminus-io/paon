import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import classNames from 'classnames';

import { LoadingIndicator } from './loading_indicator';

export default class Button extends PureComponent {

  static propTypes = {
    text: PropTypes.node,
    type: PropTypes.string,
    onClick: PropTypes.func,
    disabled: PropTypes.bool,
    block: PropTypes.bool,
    secondary: PropTypes.bool,
    dangerous: PropTypes.bool,
    autoFocus: PropTypes.bool,
    ariaBusy: PropTypes.bool,
    loading: PropTypes.bool,
    className: PropTypes.string,
    title: PropTypes.string,
    children: PropTypes.node,
  };

  static defaultProps = {
    type: 'button',
  };

  handleClick = (e) => {
    if (this.props.disabled || this.props.loading) {
      e.preventDefault();
      e.stopPropagation();
    } else if (this.props.onClick) {
      this.props.onClick(e);
    }
  };

  setRef = (c) => {
    this.node = c;
  };

  focus() {
    this.node.focus();
  }

  render () {
    const className = classNames('button', this.props.className, {
      'button-secondary': this.props.secondary,
      'button--block': this.props.block,
      'button--dangerous': this.props.dangerous,
      loading: this.props.loading,
    });
    const label = this.props.text || this.props.children;

    return (
      <button
        className={className}
        disabled={this.props.disabled && !this.props.loading}
        autoFocus={this.props.autoFocus}
        aria-busy={this.props.loading || this.props.ariaBusy}
        aria-disabled={this.props.loading || undefined}
        aria-live={this.props.loading !== undefined ? 'polite' : undefined}
        onClick={this.handleClick}
        ref={this.setRef}
        title={this.props.title}
        type={this.props.type}
      >
        {this.props.loading ? (
          <>
            <span className='button__label-wrapper'>{label}</span>
            <LoadingIndicator role='none' />
          </>
        ) : label}
      </button>
    );
  }

}
