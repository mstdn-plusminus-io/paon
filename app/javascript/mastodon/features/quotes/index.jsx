import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { defineMessages, FormattedMessage, injectIntl } from 'react-intl';

import { Helmet } from 'react-helmet';

import ImmutablePropTypes from 'react-immutable-proptypes';
import { connect } from 'react-redux';

import { debounce } from 'lodash';

import { expandQuotes, fetchQuotes } from 'mastodon/actions/interactions';
import ColumnHeader from 'mastodon/components/column_header';
import { Icon } from 'mastodon/components/icon';
import { LoadingIndicator } from 'mastodon/components/loading_indicator';
import StatusList from 'mastodon/components/status_list';

import Column from '../ui/components/column';

const messages = defineMessages({
  heading: { id: 'column.quotes', defaultMessage: 'Quotes' },
  refresh: { id: 'refresh', defaultMessage: 'Refresh' },
});

const mapStateToProps = (state, props) => ({
  statusIds: state.getIn(['status_lists', 'quotes', props.params.statusId, 'items']),
  hasMore: Boolean(state.getIn(['status_lists', 'quotes', props.params.statusId, 'next'])),
  isLoading: state.getIn(['status_lists', 'quotes', props.params.statusId, 'isLoading'], true),
});

class Quotes extends PureComponent {
  static propTypes = {
    params: PropTypes.object.isRequired,
    dispatch: PropTypes.func.isRequired,
    statusIds: ImmutablePropTypes.iterable,
    hasMore: PropTypes.bool,
    isLoading: PropTypes.bool,
    multiColumn: PropTypes.bool,
    intl: PropTypes.object.isRequired,
  };

  componentDidMount () {
    this.props.dispatch(fetchQuotes(this.props.params.statusId));
  }

  componentDidUpdate (prevProps) {
    if (prevProps.params.statusId !== this.props.params.statusId) {
      this.props.dispatch(fetchQuotes(this.props.params.statusId));
    }
  }

  handleRefresh = () => {
    this.props.dispatch(fetchQuotes(this.props.params.statusId));
  };

  handleLoadMore = debounce(() => {
    this.props.dispatch(expandQuotes(this.props.params.statusId));
  }, 300, { leading: true });

  render () {
    const { statusIds, hasMore, isLoading, multiColumn, intl } = this.props;

    if (!statusIds) {
      return <Column><LoadingIndicator /></Column>;
    }

    return (
      <Column bindToDocument={!multiColumn} label={intl.formatMessage(messages.heading)}>
        <ColumnHeader
          showBackButton
          title={intl.formatMessage(messages.heading)}
          multiColumn={multiColumn}
          extraButton={(
            <button type='button' className='column-header__button' title={intl.formatMessage(messages.refresh)} aria-label={intl.formatMessage(messages.refresh)} onClick={this.handleRefresh}>
              <Icon id='refresh' />
            </button>
          )}
        />

        <StatusList
          scrollKey={`quotes-${this.props.params.statusId}`}
          statusIds={statusIds}
          onLoadMore={this.handleLoadMore}
          hasMore={hasMore}
          isLoading={isLoading}
          emptyMessage={<FormattedMessage id='status.quotes.empty' defaultMessage='No one has quoted this post yet. When someone does, it will show up here.' />}
          bindToDocument={!multiColumn}
        />

        <Helmet>
          <title>{intl.formatMessage(messages.heading)}</title>
          <meta name='robots' content='noindex' />
        </Helmet>
      </Column>
    );
  }
}

export default connect(mapStateToProps)(injectIntl(Quotes));
