import { defineMessages, useIntl } from 'react-intl';

import AlternateEmailIcon from '@/material-icons/400-24px/alternate_email.svg?react';
import LockIcon from '@/material-icons/400-24px/lock.svg?react';
import PublicIcon from '@/material-icons/400-24px/public.svg?react';
import QuietTimeIcon from '@/material-icons/400-24px/quiet_time.svg?react';

import { Icon } from './icon';

const messages = defineMessages({
  public: { id: 'privacy.public.short', defaultMessage: 'Public' },
  unlisted: { id: 'privacy.unlisted.short', defaultMessage: 'Quiet public' },
  private: { id: 'privacy.private.short', defaultMessage: 'Followers only' },
  direct: {
    id: 'privacy.direct.short',
    defaultMessage: 'Mentioned people only',
  },
});

const icons = {
  public: { id: 'globe', component: PublicIcon },
  unlisted: { id: 'unlock', component: QuietTimeIcon },
  private: { id: 'lock', component: LockIcon },
  direct: { id: 'at', component: AlternateEmailIcon },
};

type Visibility = keyof typeof icons;

export const VisibilityIcon: React.FC<{ visibility: string }> = ({
  visibility,
}) => {
  const intl = useIntl();
  const visibilityKey: Visibility = Object.prototype.hasOwnProperty.call(
    icons,
    visibility,
  )
    ? (visibility as Visibility)
    : 'public';
  const icon = icons[visibilityKey];

  return (
    <Icon
      id={icon.id}
      icon={icon.component}
      title={intl.formatMessage(messages[visibilityKey])}
    />
  );
};
