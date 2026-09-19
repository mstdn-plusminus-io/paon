import { connect } from 'react-redux';

import { addPoll, removePoll } from '../../../actions/compose';
import PollButton from '../components/poll_button';

const mapStateToProps = state => ({
  unavailable: state.getIn(['compose', 'is_uploading']) || Boolean(state.getIn(['compose', 'quoted_status_id'])),
  active: state.getIn(['compose', 'poll']) !== null,
});

const mapDispatchToProps = dispatch => ({

  onClick () {
    dispatch((_, getState) => {
      if (getState().getIn(['compose', 'poll'])) {
        dispatch(removePoll());
      } else {
        dispatch(addPoll());
      }
    });
  },

});

export default connect(mapStateToProps, mapDispatchToProps)(PollButton);
