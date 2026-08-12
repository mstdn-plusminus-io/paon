import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { FormattedDate, FormattedMessage, defineMessages, injectIntl } from 'react-intl';

import { Helmet } from 'react-helmet';
import { Link } from 'react-router-dom';

import api from 'mastodon/api';
import Column from 'mastodon/components/column';
import { Skeleton } from 'mastodon/components/skeleton';

const messages = defineMessages({
  title: { id: 'terms_of_service.title', defaultMessage: 'Terms of Service' },
});

class TermsOfService extends PureComponent {

  static propTypes = {
    intl: PropTypes.object,
    multiColumn: PropTypes.bool,
    match: PropTypes.shape({ params: PropTypes.shape({ date: PropTypes.string }) }),
  };

  state = { content: null, effectiveDate: null, effective: false, succeededBy: null, isLoading: true, failed: false };

  componentDidMount () {
    this.loadTerms();
  }

  componentDidUpdate (previousProps) {
    if (previousProps.match?.params?.date !== this.props.match?.params?.date) {
      this.loadTerms();
    }
  }

  loadTerms = () => {
    const date = this.props.match?.params?.date;
    const path = date ? `/api/v1/instance/terms_of_service/${encodeURIComponent(date)}` : '/api/v1/instance/terms_of_service';
    this.setState({ isLoading: true, failed: false });
    api().get(path).then(({ data }) => {
      this.setState({ content: data.content, effectiveDate: data.effective_date, effective: data.effective, succeededBy: data.succeeded_by, isLoading: false });
    }).catch(() => {
      this.setState({ isLoading: false, failed: true });
    });
  };

  render () {
    const { intl, multiColumn } = this.props;
    const { content, effectiveDate, effective, succeededBy, isLoading, failed } = this.state;

    return (
      <Column bindToDocument={!multiColumn} label={intl.formatMessage(messages.title)}>
        <div className='scrollable privacy-policy'>
          <div className='column-title'>
            <h3><FormattedMessage id='terms_of_service.title' defaultMessage='Terms of Service' /></h3>
            <p>
              {isLoading ? <Skeleton width='18ch' /> : effectiveDate ? (effective ? (
                <FormattedMessage id='terms_of_service.effective' defaultMessage='Effective {date}' values={{ date: <FormattedDate value={effectiveDate} year='numeric' month='short' day='2-digit' /> }} />
              ) : (
                <FormattedMessage id='terms_of_service.effective_on' defaultMessage='Effective on {date}' values={{ date: <FormattedDate value={effectiveDate} year='numeric' month='short' day='2-digit' /> }} />
              )) : null}
            </p>
          </div>

          {failed ? (
            <div className='empty-column-indicator'><FormattedMessage id='terms_of_service.unavailable' defaultMessage='Terms of service are unavailable.' /></div>
          ) : (
            <div className='privacy-policy__body prose' dangerouslySetInnerHTML={{ __html: content }} />
          )}

          {succeededBy && (
            <div className='privacy-policy__body prose'>
              <Link to={`/terms-of-service/${succeededBy}`}><FormattedMessage id='terms_of_service.succeeded_by' defaultMessage='View the newer terms of service' /></Link>
            </div>
          )}
        </div>

        <Helmet>
          <title>{intl.formatMessage(messages.title)}</title>
          <meta name='robots' content='all' />
        </Helmet>
      </Column>
    );
  }

}

export default injectIntl(TermsOfService);
