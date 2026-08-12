import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { connect } from 'react-redux';

import { closeOnboarding } from 'mastodon/actions/onboarding';
import Column from 'mastodon/features/ui/components/column';

import Follows from './follows';
import { Profile } from './profile';

export const onboardingStepFromPath = path => path === '/start/follows' ? 'follows' : 'profile';

class Onboarding extends PureComponent {

  static contextTypes = {
    router: PropTypes.object.isRequired,
  };

  static propTypes = {
    dispatch: PropTypes.func.isRequired,
    location: PropTypes.object,
    multiColumn: PropTypes.bool,
  };

  state = {
    step: onboardingStepFromPath(this.props.location?.pathname || window.location.pathname),
  };

  componentDidUpdate (previousProps) {
    const path = this.props.location?.pathname;

    if (path && path !== previousProps.location?.pathname) {
      const step = onboardingStepFromPath(path);

      if (step !== this.state.step) {
        this.setState({ step });
      }
    }
  }

  setStep = step => {
    this.setState({ step });
    this.context.router.history.push(`/start/${step}`);
  };

  handleProfileSaved = () => {
    this.props.dispatch(closeOnboarding());
    this.setStep('follows');
  };

  handleBackToProfile = () => {
    this.setStep('profile');
  };

  handleComplete = () => {
    this.props.dispatch(closeOnboarding());
  };

  render () {
    const { multiColumn } = this.props;

    if (this.state.step === 'follows') {
      return <Follows onBack={this.handleBackToProfile} onComplete={this.handleComplete} multiColumn={multiColumn} />;
    }

    return <Column><Profile onSaved={this.handleProfileSaved} multiColumn={multiColumn} /></Column>;
  }

}

export default connect()(Onboarding);
