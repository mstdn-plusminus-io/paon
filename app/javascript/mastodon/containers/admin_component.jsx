import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { BrowserRouter } from 'react-router-dom';

import { IntlProvider } from 'mastodon/locales';

export default class AdminComponent extends PureComponent {

  static propTypes = {
    children: PropTypes.node.isRequired,
  };

  render () {
    const { children } = this.props;

    return (
      <BrowserRouter>
        <IntlProvider>
          {children}
        </IntlProvider>
      </BrowserRouter>
    );
  }

}
