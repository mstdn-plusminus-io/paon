import React from 'react';

import { throttle } from 'lodash';

import ComposeFormContainer from '../../compose/containers/compose_form_container';

export default class ComposeHalfModal extends React.Component {

  constructor() {
    super();

    this.state = {
      offsetTop: 0,
      pageTop: 0,
      height: 0,
    };
  }

  componentDidMount() {
    window.addEventListener('scroll', this.handleScroll);
    window.visualViewport.addEventListener('scroll', this.handleScroll);
    window.visualViewport.addEventListener('resize', this.handleScroll);

    this.handleScroll();
  }

  componentWillUnmount() {
    window.removeEventListener('scroll', this.handleScroll);
    window.visualViewport.removeEventListener('scroll', this.handleScroll);
    window.visualViewport.removeEventListener('resize', this.handleScroll);
  }

  handleScroll = throttle(() => {
    const { height, offsetTop, pageTop } = window.visualViewport;
    this.setState({
      offsetTop,
      pageTop,
      height,
    });
  }, 15);

  closeComposeHalfModal = () => {
    document.documentElement.classList.remove('show-compose-half-modal');
  };

  render() {
    return (
      <div className={'compose-half-modal'} style={{ height: this.state.pageTop + this.state.height }}>
        <ComposeFormContainer showClose onClose={this.closeComposeHalfModal} style={{ bottom: 0 }} />
      </div>
    );
  }

}
