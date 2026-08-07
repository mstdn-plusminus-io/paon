import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { FormattedMessage, injectIntl } from 'react-intl';

import { Link, withRouter } from 'react-router-dom';

import { connect } from 'react-redux';

import { openModal } from 'mastodon/actions/modal';
import { fetchServer } from 'mastodon/actions/server';
import { Avatar } from 'mastodon/components/avatar';
import { Icon } from 'mastodon/components/icon';
import { WordmarkLogo, SymbolLogo } from 'mastodon/components/logo';
import { registrationsOpen, me, sso_redirect } from 'mastodon/initial_state';

const Account = connect(state => ({
  account: state.getIn(['accounts', me]),
}))(({ account }) => (
  <Link to={`/@${account.get('acct')}`} title={account.get('acct')}>
    <Avatar account={account} size={35} />
  </Link>
));

const mapStateToProps = (state) => ({
  signupUrl: state.getIn(['server', 'server', 'registrations', 'url'], null) || '/auth/sign_up',
});

const mapDispatchToProps = (dispatch) => ({
  openClosedRegistrationsModal() {
    dispatch(openModal({ modalType: 'CLOSED_REGISTRATIONS' }));
  },
  dispatchServer() {
    dispatch(fetchServer());
  }
});

class Header extends PureComponent {

  static contextTypes = {
    identity: PropTypes.object,
  };

  static propTypes = {
    openClosedRegistrationsModal: PropTypes.func,
    location: PropTypes.object,
    signupUrl: PropTypes.string.isRequired,
    dispatchServer: PropTypes.func,
    intl: PropTypes.object.isRequired,
  };

  componentDidMount () {
    const { dispatchServer } = this.props;
    dispatchServer();
  }

  openComposeHalfModal = () => {
    // const scrollChildren = document.querySelector('.scrollable > div');
    // if (scrollChildren) {
    //   const { pageTop } = window.visualViewport;
    //   scrollChildren.style.top = `${-pageTop}px`;
    //   scrollChildren.dataset.pageTop = pageTop;
    // }

    setTimeout(() => {
      document.documentElement.classList.add('show-compose-half-modal');
    }, 0);
  };

  render () {
    const { signedIn } = this.context.identity;
    const { location, openClosedRegistrationsModal, signupUrl } = this.props;

    let content;

    if (signedIn) {
      const isBottomRightButton = localStorage.plusminus_config_post_button_location === 'bottom_right';
      const useHalfModal = localStorage.plusminus_config_post_half_modal === 'enabled';
      const buttonInner = (
        <>
          {isBottomRightButton && <Icon id='send' fixedWidth />}
          {!isBottomRightButton && <FormattedMessage id='compose_form.publish' defaultMessage='Publish' /> }
        </>
      );
      content = (
        <>
          {useHalfModal ? (
            // eslint-disable-next-line jsx-a11y/no-static-element-interactions -- half modal toggle requires a clickable area
            <div className={`button ${isBottomRightButton ? 'bottom_right' : ''}`} onClick={this.openComposeHalfModal} onKeyUp={this.openComposeHalfModal}>
              {buttonInner}
            </div>
          ) : location.pathname !== '/publish' && (
            <Link to='/publish' className={`button ${isBottomRightButton ? 'bottom_right' : ''}`}>
              {buttonInner}
            </Link>
          )}
          <Account />
        </>
      );
    } else {

      if (sso_redirect) {
        content = (
            <a href={sso_redirect} data-method='post' className='button button--block button-tertiary'><FormattedMessage id='sign_in_banner.sso_redirect' defaultMessage='Login or Register' /></a>
        )
      } else {
        let signupButton;

        if (registrationsOpen) {
          signupButton = (
            <a href={signupUrl} className='button'>
              <FormattedMessage id='sign_in_banner.create_account' defaultMessage='Create account' />
            </a>
          );
        } else {
          signupButton = (
            <button className='button' onClick={openClosedRegistrationsModal}>
              <FormattedMessage id='sign_in_banner.create_account' defaultMessage='Create account' />
            </button>
          );
        }

        content = (
          <>
            {signupButton}
            <a href='/auth/sign_in' className='button button-tertiary'><FormattedMessage id='sign_in_banner.sign_in' defaultMessage='Login' /></a>
          </>
        );
      }
    }

    return (
      <div className='ui__header'>
        <Link to='/' className='ui__header__logo'>
          <WordmarkLogo />
          <SymbolLogo />
        </Link>

        <div className='ui__header__links'>
          {content}
        </div>
      </div>
    );
  }

}

export default injectIntl(withRouter(connect(mapStateToProps, mapDispatchToProps)(Header)));
