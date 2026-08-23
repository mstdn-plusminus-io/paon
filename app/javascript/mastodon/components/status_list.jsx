import PropTypes from 'prop-types';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';

import { debounce } from 'lodash';

import { TIMELINE_GAP, TIMELINE_SUGGESTIONS } from 'mastodon/actions/timelines';
import RegenerationIndicator from 'mastodon/components/regeneration_indicator';
import { InlineFollowSuggestions } from 'mastodon/features/home_timeline/components/inline_follow_suggestions';
import { focusAdjacentFeedItem } from 'mastodon/utils/feed_keyboard_navigation';

import StatusContainer from '../containers/status_container';

import { LoadGap } from './load_gap';
import ScrollableList from './scrollable_list';

export default class StatusList extends ImmutablePureComponent {

  static propTypes = {
    scrollKey: PropTypes.string.isRequired,
    statusIds: ImmutablePropTypes.list.isRequired,
    featuredStatusIds: ImmutablePropTypes.list,
    onLoadMore: PropTypes.func,
    onScrollToTop: PropTypes.func,
    onScroll: PropTypes.func,
    trackScroll: PropTypes.bool,
    isLoading: PropTypes.bool,
    isPartial: PropTypes.bool,
    hasMore: PropTypes.bool,
    prepend: PropTypes.node,
    emptyMessage: PropTypes.node,
    alwaysPrepend: PropTypes.bool,
    withCounters: PropTypes.bool,
    timelineId: PropTypes.string,
    lastId: PropTypes.string,
    mediaOnly: PropTypes.bool,
  };

  static defaultProps = {
    trackScroll: true,
  };

  componentDidMount () {
    this.columnHeaderHeight = this.node?.node
      ? parseFloat(getComputedStyle(this.node.node).getPropertyValue('--column-header-height')) || 0
      : 0;
  }

  getFeaturedStatusCount = () => {
    return this.props.featuredStatusIds ? this.props.featuredStatusIds.size : 0;
  };

  getCurrentStatusIndex = (id, featured) => {
    if (featured) {
      return this.props.featuredStatusIds.indexOf(id);
    } else {
      return this.props.statusIds.indexOf(id) + this.getFeaturedStatusCount();
    }
  };

  handleMoveUp = (id, featured) => {
    const index = this.getCurrentStatusIndex(id, featured);
    this._selectChild(index, -1);
  };

  handleMoveDown = (id, featured) => {
    const index = this.getCurrentStatusIndex(id, featured);
    this._selectChild(index, 1);
  };

  handleLoadOlder = debounce(() => {
    const { statusIds, lastId, onLoadMore } = this.props;
    onLoadMore(lastId || (statusIds.size > 0 ? statusIds.last() : undefined));
  }, 300, { leading: true });

  _selectChild (index, direction) {
    focusAdjacentFeedItem(this.node?.node, index, direction, this.columnHeaderHeight);
  }

  setRef = c => {
    this.node = c;
  };

  render () {
    const { statusIds, featuredStatusIds, onLoadMore, timelineId, ...other }  = this.props;
    const { isLoading, isPartial } = other;

    if (isPartial) {
      return <RegenerationIndicator />;
    }

    let scrollableContent = (isLoading || statusIds.size > 0) ? (
      statusIds.map((statusId, index) => {
        if (statusId === TIMELINE_SUGGESTIONS) {
          return <InlineFollowSuggestions key={TIMELINE_SUGGESTIONS} />;
        }

        if (statusId === TIMELINE_GAP) {
          return (
            <LoadGap
              key={'gap:' + statusIds.get(index + 1)}
              disabled={isLoading}
              maxId={index > 0 ? statusIds.get(index - 1) : null}
              onClick={onLoadMore}
            />
          );
        }

        return (
          <StatusContainer
            key={statusId}
            id={statusId}
            onMoveUp={this.handleMoveUp}
            onMoveDown={this.handleMoveDown}
            contextType={timelineId}
            scrollKey={this.props.scrollKey}
            showThread
            withCounters={this.props.withCounters}
          />
        );
      })
    ) : null;

    if (scrollableContent && featuredStatusIds) {
      scrollableContent = featuredStatusIds.map(statusId => (
        <StatusContainer
          key={`f-${statusId}`}
          id={statusId}
          featured
          onMoveUp={this.handleMoveUp}
          onMoveDown={this.handleMoveDown}
          contextType={timelineId}
          showThread
          withCounters={this.props.withCounters}
        />
      )).concat(scrollableContent);
    }

    return (
      <ScrollableList {...other} showLoading={isLoading && statusIds.size === 0} onLoadMore={onLoadMore && this.handleLoadOlder} ref={this.setRef}>
        {scrollableContent}
      </ScrollableList>
    );
  }

}
