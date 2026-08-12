import { FormattedMessage, useIntl } from 'react-intl';

import { IconButton } from 'mastodon/components/icon_button';
import { AnnualReport } from 'mastodon/features/annual_report';

/* eslint-disable import/no-default-export */

const AnnualReportModal: React.FC<{
  year: string;
  onClose: () => void;
}> = ({ year, onClose }) => {
  const intl = useIntl();

  return (
    <div
      className='modal-root__modal annual-report-modal'
      aria-labelledby='annual-report-modal-title'
      role='dialog'
    >
      <div className='annual-report-modal__header'>
        <h1 id='annual-report-modal-title'>
          <FormattedMessage
            id='annual_report.title'
            defaultMessage='{year} annual report'
            values={{ year }}
          />
        </h1>
        <IconButton
          className='annual-report-modal__close'
          icon='times'
          title={intl.formatMessage({
            id: 'annual_report.close',
            defaultMessage: 'Close annual report',
          })}
          onClick={onClose}
          size={20}
        />
      </div>
      <div className='annual-report-modal__body'>
        <AnnualReport year={year} />
      </div>
    </div>
  );
};

export default AnnualReportModal;
