import type { ApiAccountJSON } from 'mastodon/api_types/notifications';

import reducer from '../notification_groups';
import type { NotificationGap } from '../notification_groups';

const account = (id: string): ApiAccountJSON => ({
  id,
  acct: id,
  username: id,
  display_name: id,
  avatar: '',
  avatar_static: '',
});

const group = (groupKey: string, id: string, overrides = {}) => ({
  group_key: groupKey,
  notifications_count: 1,
  type: 'favourite',
  sample_account_ids: [`account-${id}`],
  most_recent_notification_id: id,
  page_min_id: id,
  page_max_id: id,
  latest_page_notification_at: `2026-01-01T00:00:0${id}Z`,
  status_id: 'status-1',
  ...overrides,
});

describe('notification groups reducer', () => {
  it('keeps a next-page gap and merges a group spanning page boundaries', () => {
    let state = reducer(undefined, {
      type: 'NOTIFICATION_GROUPS_FETCH_SUCCESS',
      mode: 'replace',
      groups: [
        group('favourite-status-1', '9'),
        group('favourite-status-2', '8'),
      ],
      next: '/api/v2/notifications?max_id=8',
    });
    const gap = state.groups.at(-1) as NotificationGap;

    state = reducer(state, {
      type: 'NOTIFICATION_GROUPS_FETCH_SUCCESS',
      mode: 'gap',
      gap,
      groups: [
        group('favourite-status-2', '8', {
          notifications_count: 3,
          sample_account_ids: ['account-8', 'account-7'],
        }),
        group('favourite-status-3', '6'),
      ],
    });

    const groups = state.groups.filter((item) => item.kind === 'notification');
    expect(
      groups.map((item) => item.kind === 'notification' && item.group_key),
    ).toEqual([
      'favourite-status-1',
      'favourite-status-2',
      'favourite-status-3',
    ]);
    expect(
      groups[1]?.kind === 'notification' && groups[1].notifications_count,
    ).toBe(3);
    expect(state.groups.some((item) => item.kind === 'gap')).toBe(false);
  });

  it('merges streaming activity into a pending group in slow mode', () => {
    let state = reducer(undefined, {
      type: 'NOTIFICATION_GROUPS_PROCESS_NEW',
      usePendingItems: true,
      groupedTypes: ['favourite'],
      notification: {
        id: '10',
        type: 'favourite',
        group_key: 'favourite-status-1',
        created_at: '2026-01-01T00:00:00Z',
        account: account('account-1'),
        status: { id: 'status-1', account: account('author') },
      },
    });
    state = reducer(state, {
      type: 'NOTIFICATION_GROUPS_PROCESS_NEW',
      usePendingItems: true,
      groupedTypes: ['favourite'],
      notification: {
        id: '11',
        type: 'favourite',
        group_key: 'favourite-status-1',
        created_at: '2026-01-01T00:01:00Z',
        account: account('account-2'),
        status: { id: 'status-1', account: account('author') },
      },
    });

    expect(state.pendingGroups).toHaveLength(1);
    expect(state.pendingGroups[0]?.notifications_count).toBe(2);
    expect(state.pendingGroups[0]?.sampleAccountIds).toEqual([
      'account-2',
      'account-1',
    ]);

    state = reducer(state, { type: 'NOTIFICATION_GROUPS_LOAD_PENDING' });
    expect(state.pendingGroups).toHaveLength(0);
    expect(state.groups[0]?.kind).toBe('notification');
  });

  it('keeps unknown and null-account notifications as safe ungrouped records', () => {
    const state = reducer(undefined, {
      type: 'NOTIFICATION_GROUPS_PROCESS_NEW',
      usePendingItems: false,
      groupedTypes: ['favourite'],
      notification: {
        id: '12',
        type: 'future.notification',
        created_at: '2026-01-01T00:00:00Z',
        account: null,
      },
    });

    expect(state.groups[0]).toMatchObject({
      kind: 'notification',
      group_key: 'ungrouped-12',
      type: 'future.notification',
      sampleAccountIds: [],
    });
  });

  it('does not auto-read across an unloaded pagination gap', () => {
    let state = reducer(undefined, {
      type: 'MARKERS_FETCH_SUCCESS',
      markers: { notifications: { last_read_id: '5' } },
    });
    state = reducer(state, { type: 'NOTIFICATION_GROUPS_MOUNT' });
    state = reducer(state, {
      type: 'NOTIFICATION_GROUPS_SCROLL',
      top: true,
    });
    state = reducer(state, {
      type: 'NOTIFICATION_GROUPS_FETCH_SUCCESS',
      mode: 'replace',
      groups: [group('favourite-status-1', '10'), group('other', '9')],
      next: '/api/v2/notifications?max_id=9',
    });

    expect(state.lastReadId).toBe('5');
    const gap = state.groups.at(-1) as NotificationGap;
    state = reducer(state, {
      type: 'NOTIFICATION_GROUPS_FETCH_SUCCESS',
      mode: 'gap',
      gap,
      groups: [group('older', '4')],
    });

    expect(state.lastReadId).toBe('10');
    expect(state.readMarkerId).toBe('5');
    state = reducer(state, { type: 'APP_UNFOCUS' });
    state = reducer(state, { type: 'APP_FOCUS' });
    expect(state.readMarkerId).toBe('10');
  });
});
