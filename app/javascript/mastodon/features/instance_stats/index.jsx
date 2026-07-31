import 'moment';
import 'chartjs-adapter-moment';

import PropTypes from 'prop-types';
import React from 'react';

import { defineMessages, injectIntl } from 'react-intl';

import { Helmet } from 'react-helmet';

import ImmutablePropTypes from 'react-immutable-proptypes';
import { connect } from 'react-redux';

import { Chart as ChartJS, registerables } from 'chart.js';
import { Line } from 'react-chartjs-2';

import { fetchInstanceStats } from 'mastodon/actions/instance_stats';
import Column from 'mastodon/components/column';
import { Skeleton } from 'mastodon/components/skeleton';

import { buildDeliveryStatDatasets, buildDeliveryStatsChartOptions } from './chart';

ChartJS.register(...registerables);

const messages = defineMessages({
  title: { id: 'column.instance_stats', defaultMessage: 'Instance statistics' },
  delivery_stats: { id: 'instance_stats.delivery_stats', defaultMessage: 'Delivery statistics' },
});

const mapStateToProps = state => ({
  instance_stats: state.getIn(['instance_stats', 'instance_stats']),
});

const renderStats = (header, stats) => {
  const chartOptions = buildDeliveryStatsChartOptions(stats.delivery_histories);

  const chart = <Line options={chartOptions} data={buildDeliveryStatDatasets(stats.delivery_histories)} />;
  ChartJS.defaults.color = '#bbb';

  return (
    <div className='about__header'>
      <p>{header}</p>

      {chart}
    </div>
  );
};

class InstanceStats extends React.PureComponent {

  static propTypes = {
    params: PropTypes.object.isRequired,
    instance_stats: ImmutablePropTypes.map,
    dispatch: PropTypes.func.isRequired,
    intl: PropTypes.object.isRequired,
    multiColumn: PropTypes.bool,
  };

  componentDidMount() {
    const { dispatch, params } = this.props;
    dispatch(fetchInstanceStats(params.domain));
  }

  render() {
    const { multiColumn, intl, instance_stats } = this.props;
    const domain = this.props.params.domain;
    const isLoading = instance_stats.get('isLoading');
    const stats = instance_stats?.get('instance_stats');
    const title = intl.formatMessage(messages.title);

    return (
      <Column bindToDocument={!multiColumn} label={title}>
        <div className='scrollable about'>
          <div className='about__header'>
            <h1>{title}</h1>
            <p>{isLoading ? <Skeleton width='10ch' /> : domain}</p>
          </div>

          {isLoading ? null : renderStats(intl.formatMessage(messages.delivery_stats), stats.toJS())}

        </div>

        <Helmet>
          <title>{`${domain} | ${title}`}</title>
          <meta name='robots' content='noarchive' />
        </Helmet>
      </Column>
    );
  }

}

export default connect(mapStateToProps)(injectIntl(InstanceStats))
