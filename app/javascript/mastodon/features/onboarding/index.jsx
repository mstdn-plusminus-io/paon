import PropTypes from 'prop-types';

import { FormattedMessage, injectIntl, defineMessages } from 'react-intl';

import { Helmet } from 'react-helmet';
import { Link } from 'react-router-dom';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';
import { connect } from 'react-redux';

import { debounce } from 'lodash';

import illustration from 'mastodon/../images/elephant_ui_conversation.svg';
import { fetchAccount } from 'mastodon/actions/accounts';
import { focusCompose } from 'mastodon/actions/compose';
import { closeOnboarding } from 'mastodon/actions/onboarding';
import { changeSetting } from 'mastodon/actions/settings';
import Column from 'mastodon/features/ui/components/column';
import { me } from 'mastodon/initial_state';
import { makeGetAccount } from 'mastodon/selectors';
import { assetHost } from 'mastodon/utils/config';

import ArrowSmallRight from './components/arrow_small_right';
import Step from './components/step';
import Follows from './follows';
import { Profile } from './profile';
import Share from './share';

const messages = defineMessages({
  template: { id: 'onboarding.compose.template', defaultMessage: 'Hello #Mastodon!' },
});

const mapStateToProps = () => {
  const getAccount = makeGetAccount();

  return state => ({
    account: getAccount(state, me),
    lastStep: state.getIn(['settings', 'onboarding', 'last_step']),
    shareCompleted: state.getIn(['settings', 'onboarding', 'share_completed'], false),
  });
};

class Onboarding extends ImmutablePureComponent {

  static contextTypes = {
    router: PropTypes.object.isRequired,
  };

  static propTypes = {
    dispatch: PropTypes.func.isRequired,
    account: ImmutablePropTypes.map,
    multiColumn: PropTypes.bool,
    lastStep: PropTypes.string,
    shareCompleted: PropTypes.bool,
  };

  state = {
    step: window.location.pathname.startsWith('/start/') ? window.location.pathname.slice('/start/'.length).split('/')[0] : null,
  };

  handleComplete = () => {
    this.props.dispatch(closeOnboarding());
  };

  handleGoToExplore = () => {
    this.props.dispatch(closeOnboarding());
  };

  handleGoToHome = () => {
    this.props.dispatch(closeOnboarding());
  };

  handleProfileClick = () => {
    this.setStep('profile');
  };

  handleFollowClick = () => {
    this.setStep('follows');
  };

  handleComposeClick = () => {
    const { dispatch, intl } = this.props;
    const { router } = this.context;

    dispatch(focusCompose(router.history, intl.formatMessage(messages.template)));
  };

  handleShareClick = () => {
    this.props.dispatch(changeSetting(['onboarding', 'share_completed'], true));
    this.setStep('share');
  };

  handleBackClick = () => {
    this.setStep(null);
  };

  handleProfileSaved = () => {
    this.setStep('follows');
  };

  setStep = step => {
    this.props.dispatch(changeSetting(['onboarding', 'last_step'], step));
    this.setState({ step });
    this.context.router.history.push(step ? `/start/${step}` : '/start');
  };

  handleWindowFocus = debounce(() => {
    const { dispatch, account } = this.props;
    dispatch(fetchAccount(account.get('id')));
  }, 1000, { trailing: true });

  componentDidMount () {
    window.addEventListener('focus', this.handleWindowFocus, false);
    if (!this.state.step && this.props.lastStep && ['profile', 'follows', 'share'].includes(this.props.lastStep)) {
      this.setState({ step: this.props.lastStep });
      this.context.router.history.replace(`/start/${this.props.lastStep}`);
    }
  }

  componentWillUnmount () {
    window.removeEventListener('focus', this.handleWindowFocus);
  }

  render () {
    const { account, multiColumn, shareCompleted } = this.props;
    const { step } = this.state;

    switch(step) {
    case 'profile':
      return <Column><Profile onBack={this.handleBackClick} onSaved={this.handleProfileSaved} multiColumn={multiColumn} /></Column>;
    case 'follows':
      return <Follows onBack={this.handleBackClick} multiColumn={multiColumn} />;
    case 'share':
      return <Share onBack={this.handleBackClick} onComplete={this.handleComplete} multiColumn={multiColumn} />;
    }

    return (
      <Column>
        <div className='scrollable privacy-policy'>
          <div className='column-title'>
            <img src={illustration} alt='' className='onboarding__illustration' />
            <h3><FormattedMessage id='onboarding.start.title' defaultMessage="You've made it!" /></h3>
            <p><FormattedMessage id='onboarding.start.lead' defaultMessage="Your new Mastodon account is ready to go. Here's how you can make the most of it:" /></p>
          </div>

          <div className='onboarding__steps'>
            <Step onClick={this.handleProfileClick} completed={(!account.get('avatar').endsWith('missing.png')) || (account.get('display_name').length > 0 && account.get('note').length > 0)} icon='address-book-o' label={<FormattedMessage id='onboarding.steps.setup_profile.title' defaultMessage='Customize your profile' />} description={<FormattedMessage id='onboarding.steps.setup_profile.body' defaultMessage='Others are more likely to interact with you with a filled out profile.' />} />
            <Step onClick={this.handleFollowClick} completed={(account.get('following_count') * 1) >= 7} icon='user-plus' label={<FormattedMessage id='onboarding.steps.follow_people.title' defaultMessage='Find at least {count, plural, one {one person} other {# people}} to follow' values={{ count: 7 }} />} description={<FormattedMessage id='onboarding.steps.follow_people.body' defaultMessage="You curate your own home feed. Let's fill it with interesting people." />} />
            <Step onClick={this.handleComposeClick} completed={(account.get('statuses_count') * 1) >= 1} icon='pencil-square-o' label={<FormattedMessage id='onboarding.steps.publish_status.title' defaultMessage='Make your first post' />} description={<FormattedMessage id='onboarding.steps.publish_status.body' defaultMessage='Say hello to the world.' values={{ emoji: <img className='emojione' alt='🐘' src={`${assetHost}/emoji/1f418.svg`} /> }} />} />
            <Step onClick={this.handleShareClick} completed={shareCompleted} icon='copy' label={<FormattedMessage id='onboarding.steps.share_profile.title' defaultMessage='Share your profile' />} description={<FormattedMessage id='onboarding.steps.share_profile.body' defaultMessage='Let your friends know how to find you on Mastodon!' />} />
          </div>

          <p className='onboarding__lead'><FormattedMessage id='onboarding.start.skip' defaultMessage="Don't need help getting started?" /></p>

          <div className='onboarding__links'>
            <Link to='/explore' className='onboarding__link' onClick={this.handleGoToExplore}>
              <FormattedMessage id='onboarding.actions.go_to_explore' defaultMessage='Take me to trending' />
              <ArrowSmallRight />
            </Link>

            <Link to='/home' className='onboarding__link' onClick={this.handleGoToHome}>
              <FormattedMessage id='onboarding.actions.go_to_home' defaultMessage='Take me to my home feed' />
              <ArrowSmallRight />
            </Link>
          </div>
        </div>

        <Helmet>
          <meta name='robots' content='noindex' />
        </Helmet>
      </Column>
    );
  }

}

export default connect(mapStateToProps)(injectIntl(Onboarding));
