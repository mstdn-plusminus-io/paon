const deliverySeries = [
  {
    key: 'success',
    color: '#36A2EB',
  },
  {
    key: 'failure',
    color: '#FF6384',
  },
];

export const buildDeliveryStatDatasets = stats => {
  const datasets = deliverySeries.map(series => {
    const data = stats.map(stat => ({
      x: stat.time,
      y: stat[`${series.key}_count`],
    }));

    return {
      label: series.key,
      data,
      pointStyle: 'circle',
      pointRadius: data.map(point => point.y > 0 ? 2 : 0),
      pointHoverRadius: 5,
      tension: 0.1,
      borderColor: series.color,
    };
  });

  return { datasets };
};

export const buildDeliveryStatsChartOptions = stats => {
  const maximumCount = stats.reduce((maximum, stat) => {
    return Math.max(maximum, stat.success_count, stat.failure_count);
  }, 0);

  const yScale = {
    beginAtZero: true,
    ticks: {
      precision: 0,
    },
  };

  if (maximumCount === 0) {
    yScale.suggestedMax = 1;
  }

  return {
    interaction: {
      intersect: false,
      mode: 'index',
    },
    plugins: {
      legend: {
        position: 'bottom',
        labels: {
          usePointStyle: true,
          pointStyle: 'rounded',
        },
      },
      tooltip: {
        position: 'nearest',
      },
    },
    scales: {
      x: {
        type: 'time',
        time: {
          unit: 'day',
          tooltipFormat: 'YYYY/MM/DD HH:00',
          displayFormats: {
            hour: 'DD HH:00',
            day: 'MM/DD',
          },
        },
      },
      y: yScale,
    },
  };
};
