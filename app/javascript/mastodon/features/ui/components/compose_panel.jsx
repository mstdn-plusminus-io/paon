import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { connect } from 'react-redux';

import { changeComposing, mountCompose, unmountCompose } from 'mastodon/actions/compose';
import ServerBanner from 'mastodon/components/server_banner';
import ComposeFormContainer from 'mastodon/features/compose/containers/compose_form_container';
import NavigationContainer from 'mastodon/features/compose/containers/navigation_container';
import SearchContainer from 'mastodon/features/compose/containers/search_container';
import { identityContextPropShape, withIdentity } from 'mastodon/identity_context';

import { shouldHideComposePanel } from './compose_panel_utils';
import LinkFooter from './link_footer';

export class ComposePanel extends PureComponent {

  static contextTypes = {
  };

  static propTypes = {
    identity: identityContextPropShape,
    dispatch: PropTypes.func.isRequired,
    hideComposer: PropTypes.bool,
  };

  onFocus = () => {
    const { dispatch } = this.props;
    dispatch(changeComposing(true));
  };

  onBlur = () => {
    const { dispatch } = this.props;
    dispatch(changeComposing(false));
  };

  componentDidMount () {
    const { dispatch } = this.props;
    dispatch(mountCompose());
  }

  componentWillUnmount () {
    const { dispatch } = this.props;
    dispatch(unmountCompose());
  }

  render() {
    const { signedIn } = this.props.identity;
    const { hideComposer } = this.props;

    return (
      <div className='compose-panel' onFocus={this.onFocus}>
        <SearchContainer openInRoute />

        {!signedIn && (
          <>
            <ServerBanner />
            <div className='flex-spacer' />
          </>
        )}

        {signedIn && (
          <>
            <NavigationContainer onClose={this.onBlur} />
            {!hideComposer && <ComposeFormContainer singleColumn />}
            {hideComposer && <div className='compose-form' />}
          </>
        )}

        <LinkFooter />
      </div>
    );
  }

}

const mapStateToProps = state => ({
  hideComposer: shouldHideComposePanel(state.getIn(['compose', 'mounted'])),
});

export default connect(mapStateToProps)(withIdentity(ComposePanel));
