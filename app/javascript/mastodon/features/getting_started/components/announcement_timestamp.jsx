import PropTypes from 'prop-types';

import { FormattedDate } from 'react-intl';

import ImmutablePropTypes from 'react-immutable-proptypes';

export const AnnouncementTimestamp = ({ announcement, now = new Date() }) => {
  const startsAt = announcement.get('starts_at') && new Date(announcement.get('starts_at'));
  const endsAt = announcement.get('ends_at') && new Date(announcement.get('ends_at'));
  const hasTimeRange = startsAt && endsAt;
  const skipTime = announcement.get('all_day');

  if (hasTimeRange) {
    const skipYear = startsAt.getFullYear() === endsAt.getFullYear() && endsAt.getFullYear() === now.getFullYear();
    const skipEndDate = startsAt.getDate() === endsAt.getDate() && startsAt.getMonth() === endsAt.getMonth() && startsAt.getFullYear() === endsAt.getFullYear();

    return (
      <>
        <FormattedDate value={startsAt} hour12={false} year={(skipYear || startsAt.getFullYear() === now.getFullYear()) ? undefined : 'numeric'} month='short' day='2-digit' hour={skipTime ? undefined : '2-digit'} minute={skipTime ? undefined : '2-digit'} /> - <FormattedDate value={endsAt} hour12={false} year={(skipYear || endsAt.getFullYear() === now.getFullYear()) ? undefined : 'numeric'} month={skipEndDate ? undefined : 'short'} day={skipEndDate ? undefined : '2-digit'} hour={skipTime ? undefined : '2-digit'} minute={skipTime ? undefined : '2-digit'} />
      </>
    );
  }

  const publishedAt = new Date(announcement.get('published_at'));

  return <FormattedDate value={publishedAt} hour12={false} year={publishedAt.getFullYear() === now.getFullYear() ? undefined : 'numeric'} month='short' day='2-digit' hour={skipTime ? undefined : '2-digit'} minute={skipTime ? undefined : '2-digit'} />;
};

AnnouncementTimestamp.propTypes = {
  announcement: ImmutablePropTypes.map.isRequired,
  now: PropTypes.instanceOf(Date),
};
