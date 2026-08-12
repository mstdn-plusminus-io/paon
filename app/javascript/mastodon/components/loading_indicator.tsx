import { defineMessages, useIntl } from 'react-intl';

import { CircularProgress } from './circular_progress';

const messages = defineMessages({
  loading: { id: 'loading_indicator.label', defaultMessage: 'Loading…' },
});

interface LoadingIndicatorProps {
  role?: string;
}

export const LoadingIndicator: React.FC<LoadingIndicatorProps> = ({
  role = 'progressbar',
}) => {
  const intl = useIntl();
  const a11yProps =
    role === 'progressbar'
      ? ({
          role,
          'aria-busy': true,
          'aria-live': 'polite',
        } as const)
      : undefined;

  return (
    <div
      className='loading-indicator'
      {...a11yProps}
      aria-label={intl.formatMessage(messages.loading)}
    >
      <CircularProgress size={50} strokeWidth={6} />
    </div>
  );
};
