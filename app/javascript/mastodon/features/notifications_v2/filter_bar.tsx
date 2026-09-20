/* eslint-disable react/jsx-no-bind */

import { FormattedMessage } from 'react-intl';

import { setNotificationGroupsFilter } from 'mastodon/actions/notification_groups';
import { Icon } from 'mastodon/components/icon';
import { useAppDispatch, useAppSelector } from 'mastodon/store';

const FilterButton: React.FC<{
  type: string;
  active: string;
  icon?: string;
  children?: React.ReactNode;
}> = ({ type, active, icon, children }) => {
  const dispatch = useAppDispatch();
  return (
    <button
      className={active === type ? 'active' : ''}
      onClick={() => {
        dispatch(setNotificationGroupsFilter(type));
      }}
    >
      {icon ? <Icon id={icon} fixedWidth /> : children}
    </button>
  );
};

export const FilterBar: React.FC = () => {
  const active = useAppSelector(
    (state) =>
      (state.getIn(['settings', 'notifications', 'quickFilter', 'active']) as
        | string
        | undefined) ?? 'all',
  );
  const advanced = useAppSelector(
    (state) =>
      (state.getIn(['settings', 'notifications', 'quickFilter', 'advanced']) as
        | boolean
        | undefined) ?? false,
  );

  return (
    <div className='notification__filter-bar'>
      <FilterButton type='all' active={active}>
        <FormattedMessage id='notifications.filter.all' defaultMessage='All' />
      </FilterButton>
      <FilterButton
        type='mention'
        active={active}
        icon={advanced ? 'reply-all' : undefined}
      >
        <FormattedMessage
          id='notifications.filter.mentions'
          defaultMessage='Mentions'
        />
      </FilterButton>
      {advanced && (
        <>
          <FilterButton type='favourite' active={active} icon='star' />
          <FilterButton type='reblog' active={active} icon='retweet' />
          <FilterButton type='poll' active={active} icon='tasks' />
          <FilterButton type='status' active={active} icon='home' />
          <FilterButton type='follow' active={active} icon='user-plus' />
        </>
      )}
    </div>
  );
};
