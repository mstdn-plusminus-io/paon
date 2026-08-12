import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { FormattedMessage, defineMessages, injectIntl } from 'react-intl';

import { Helmet } from 'react-helmet';

import { connect } from 'react-redux';

import { expandLinkTimeline } from 'mastodon/actions/timelines';
import Column from 'mastodon/components/column';
import ColumnHeader from 'mastodon/components/column_header';
import StatusListContainer from 'mastodon/features/ui/containers/status_list_container';

import { decodeLinkTimelineURL } from './url';

const messages = defineMessages({
  title: { id: 'link_timeline.title', defaultMessage: 'Posts sharing a link' },
});

const makeMapStateToProps = () => (state, { params }) => {
  const url = decodeLinkTimelineURL(params.url);
  const timelineId = `link:${url || ''}`;
  const firstStatusId = url ? state.getIn(['timelines', timelineId, 'items', 0]) : null;

  return {
    url,
    timelineId,
    storyTitle: firstStatusId ? state.getIn(['statuses', firstStatusId, 'card', 'title']) : null,
    hasError: !url || !!state.getIn(['timelines', timelineId, 'error']),
  };
};

class LinkTimeline extends PureComponent {

  static propTypes = {
    dispatch: PropTypes.func.isRequired,
    hasError: PropTypes.bool.isRequired,
    intl: PropTypes.object.isRequired,
    multiColumn: PropTypes.bool,
    storyTitle: PropTypes.string,
    timelineId: PropTypes.string.isRequired,
    url: PropTypes.string,
  };

  componentDidMount () {
    this.load();
  }

  componentDidUpdate (prevProps) {
    if (prevProps.url !== this.props.url) {
      this.load();
    }
  }

  load = () => {
    const { dispatch, url } = this.props;

    if (url) {
      dispatch(expandLinkTimeline(url));
    }
  };

  setRef = column => {
    this.column = column;
  };

  handleHeaderClick = () => {
    this.column?.scrollTop();
  };

  handleLoadMore = maxId => {
    const { dispatch, url } = this.props;

    if (url) {
      dispatch(expandLinkTimeline(url, { maxId }));
    }
  };

  render () {
    const { hasError, intl, multiColumn, storyTitle, timelineId } = this.props;
    const title = storyTitle || intl.formatMessage(messages.title);
    const emptyMessage = hasError ? (
      <FormattedMessage id='link_timeline.error' defaultMessage='This link timeline could not be loaded.' />
    ) : (
      <FormattedMessage id='empty_column.link_timeline' defaultMessage='No posts sharing this link were found.' />
    );

    return (
      <Column bindToDocument={!multiColumn} ref={this.setRef} label={title}>
        <ColumnHeader
          icon='link'
          title={title}
          onClick={this.handleHeaderClick}
          multiColumn={multiColumn}
          showBackButton
        />

        <StatusListContainer
          timelineId={timelineId}
          onLoadMore={this.handleLoadMore}
          trackScroll
          scrollKey={`link_timeline-${timelineId}`}
          emptyMessage={emptyMessage}
          bindToDocument={!multiColumn}
        />

        <Helmet>
          <title>{title}</title>
          <meta name='robots' content='noindex' />
        </Helmet>
      </Column>
    );
  }

}

export default connect(makeMapStateToProps)(injectIntl(LinkTimeline));
