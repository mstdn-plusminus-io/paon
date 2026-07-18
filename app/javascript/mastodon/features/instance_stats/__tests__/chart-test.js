import { buildDeliveryStatDatasets, buildDeliveryStatsChartOptions } from '../chart';

const deliveryHistories = [
  {
    time: '2026-07-18T16:00:00.000Z',
    success_count: 0,
    failure_count: 0,
  },
  {
    time: '2026-07-18T17:00:00.000Z',
    success_count: 1,
    failure_count: 0,
  },
  {
    time: '2026-07-18T18:00:00.000Z',
    success_count: 3,
    failure_count: 2,
  },
];

describe('instance stats chart', () => {
  describe('buildDeliveryStatDatasets', () => {
    it('maps success and failure histories to separate datasets', () => {
      const { datasets } = buildDeliveryStatDatasets(deliveryHistories);

      expect(datasets).toHaveLength(2);
      expect(datasets[0].label).toBe('success');
      expect(datasets[0].data).toEqual([
        { x: deliveryHistories[0].time, y: 0 },
        { x: deliveryHistories[1].time, y: 1 },
        { x: deliveryHistories[2].time, y: 3 },
      ]);
      expect(datasets[1].label).toBe('failure');
      expect(datasets[1].data).toEqual([
        { x: deliveryHistories[0].time, y: 0 },
        { x: deliveryHistories[1].time, y: 0 },
        { x: deliveryHistories[2].time, y: 2 },
      ]);
    });

    it('shows points only for non-zero values', () => {
      const { datasets } = buildDeliveryStatDatasets(deliveryHistories);

      expect(datasets[0].pointRadius).toEqual([0, 2, 2]);
      expect(datasets[1].pointRadius).toEqual([0, 0, 2]);
    });
  });

  describe('buildDeliveryStatsChartOptions', () => {
    it('uses a stable unit range when all values are zero', () => {
      const options = buildDeliveryStatsChartOptions([deliveryHistories[0]]);

      expect(options.scales.y).toEqual({
        beginAtZero: true,
        suggestedMax: 1,
        ticks: { precision: 0 },
      });
    });

    it('lets Chart.js scale non-zero values without a fixed maximum', () => {
      const options = buildDeliveryStatsChartOptions(deliveryHistories);

      expect(options.scales.y.beginAtZero).toBe(true);
      expect(options.scales.y.ticks.precision).toBe(0);
      expect(options.scales.y).not.toHaveProperty('suggestedMax');
    });
  });
});
