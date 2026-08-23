import { Map as ImmutableMap, List as ImmutableList } from 'immutable';
import { connect } from 'react-redux';
import { createSelector } from 'reselect';

import { debounce } from 'lodash';

import { scrollTopTimeline, loadPending, TIMELINE_SUGGESTIONS } from '../../../actions/timelines';
import StatusList from '../../../components/status_list';
import { me } from '../../../initial_state';

const makeGetStatusIds = (pending = false) => createSelector([
  (state, _, { type }) => state.getIn(['settings', type], ImmutableMap()),
  (state, _, { type }) => state.getIn(['timelines', type, pending ? 'pendingItems' : 'items'], ImmutableList()),
  (state)           => state.get('statuses'),
  (_, props) => props,
], (columnSettings, statusIds, statuses, props) => {
  return statusIds.filter(id => {
    if (id === null || id === TIMELINE_SUGGESTIONS) return true;

    const statusForId = statuses.get(id);
    let showStatus    = true;

    if (statusForId.get('account') === me) return true;

    if (columnSettings.getIn(['shows', 'reblog']) === false) {
      showStatus = showStatus && statusForId.get('reblog') === null;
    }

    if (columnSettings.getIn(['shows', 'reply']) === false) {
      showStatus = showStatus && (statusForId.get('in_reply_to_id') === null || statusForId.get('in_reply_to_account_id') === me);
    }

    if (columnSettings.getIn(['shows', 'quote']) === false) {
      showStatus = showStatus && statusForId.get('quote') === null;
    }

    if (props.mediaOnly === true) {
      if (statusForId.get('reblog')) {
        showStatus = showStatus && statuses.get(statusForId.get('reblog')).get('media_attachments')?.size > 0;
      } else {
        showStatus = showStatus && statusForId.get('media_attachments')?.size > 0;
      }
    }

    return showStatus;
  });
});

const makeMapStateToProps = () => {
  const getStatusIds = makeGetStatusIds();
  const getPendingStatusIds = makeGetStatusIds(true);

  const mapStateToProps = (state, props) => ({
    statusIds: getStatusIds(state, props, { type: props.timelineId }),
    lastId:    state.getIn(['timelines', props.timelineId, 'items'])?.last(),
    isLoading: state.getIn(['timelines', props.timelineId, 'isLoading'], true),
    isPartial: state.getIn(['timelines', props.timelineId, 'isPartial'], false),
    hasMore:   state.getIn(['timelines', props.timelineId, 'hasMore']),
    numPending: getPendingStatusIds(state, props, { type: props.timelineId }).size,
  });

  return mapStateToProps;
};

const mapDispatchToProps = (dispatch, { timelineId }) => ({

  onScrollToTop: debounce(() => {
    dispatch(scrollTopTimeline(timelineId, true));
  }, 100),

  onScroll: debounce(() => {
    dispatch(scrollTopTimeline(timelineId, false));
  }, 100),

  onLoadPending: () => dispatch(loadPending(timelineId)),

});

export default connect(makeMapStateToProps, mapDispatchToProps)(StatusList);
