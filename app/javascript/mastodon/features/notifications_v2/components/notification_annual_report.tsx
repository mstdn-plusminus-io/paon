/* eslint-disable jsx-a11y/no-noninteractive-tabindex */

import { useCallback } from 'react';

import { FormattedMessage } from 'react-intl';

import classNames from 'classnames';

import { openModal } from 'mastodon/actions/modal';
import type { ApiAnnualReportEventJSON } from 'mastodon/api_types/notifications';
import { Icon } from 'mastodon/components/icon';
import { useAppDispatch } from 'mastodon/store';

export const NotificationAnnualReport: React.FC<{
  annualReport: ApiAnnualReportEventJSON;
  unread: boolean;
}> = ({ annualReport, unread }) => {
  const dispatch = useAppDispatch();
  const year = annualReport.year;
  const handleOpen = useCallback(() => {
    dispatch(
      openModal({
        modalType: 'ANNUAL_REPORT',
        modalProps: { year },
      }),
    );
  }, [dispatch, year]);

  return (
    <div
      className={classNames(
        'notification notification-annual-report focusable',
        { unread },
      )}
      tabIndex={0}
    >
      <div className='notification__message'>
        <div className='notification__favourite-icon-wrapper'>
          <Icon id='calendar' fixedWidth />
        </div>
        <span>
          <FormattedMessage
            id='notification.annual_report.message'
            defaultMessage="Your {year} #Wrapstodon awaits! Unveil your year's highlights and memorable moments on Mastodon!"
            values={{ year }}
          />
        </span>
      </div>
      <div className='notification-43__details'>
        <button type='button' className='button' onClick={handleOpen}>
          <FormattedMessage
            id='notification.annual_report.view'
            defaultMessage='View #Wrapstodon'
          />
        </button>
      </div>
    </div>
  );
};
