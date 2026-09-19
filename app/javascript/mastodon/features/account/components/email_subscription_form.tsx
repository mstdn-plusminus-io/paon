import { useCallback, useState } from 'react';

import { FormattedMessage, useIntl } from 'react-intl';

import { apiRequestPost } from 'mastodon/api';
import Button from 'mastodon/components/button';

export const EmailSubscriptionForm: React.FC<{ accountId: string }> = ({
  accountId,
}) => {
  const intl = useIntl();
  const [email, setEmail] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [submitted, setSubmitted] = useState(false);
  const [failed, setFailed] = useState(false);

  const submit = useCallback(async () => {
    if (!email.trim() || submitting) return;
    setSubmitting(true);
    setFailed(false);
    try {
      await apiRequestPost(`v1/accounts/${accountId}/email_subscriptions`, {
        email: email.trim(),
      });
      setSubmitted(true);
    } catch {
      setFailed(true);
    } finally {
      setSubmitting(false);
    }
  }, [accountId, email, submitting]);
  const handleEmailChange = useCallback<
    React.ChangeEventHandler<HTMLInputElement>
  >((event) => {
    setEmail(event.target.value);
  }, []);

  if (submitted) {
    return (
      <p className='account__email-subscription' role='status'>
        <FormattedMessage
          id='email_subscriptions.submitted.lead'
          defaultMessage='Check your inbox to finish subscribing.'
        />
      </p>
    );
  }

  return (
    <div className='account__email-subscription'>
      <label>
        <FormattedMessage
          id='email_subscriptions.form.title'
          defaultMessage='Get new posts by email'
        />
        <input
          type='email'
          value={email}
          placeholder={intl.formatMessage({
            id: 'email_subscriptions.email',
            defaultMessage: 'Email',
          })}
          onChange={handleEmailChange}
        />
      </label>
      <Button
        disabled={!email.trim() || submitting}
        text={intl.formatMessage({
          id: 'email_subscriptions.form.action',
          defaultMessage: 'Subscribe',
        })}
        onClick={submit}
      />
      {failed && (
        <p role='alert'>
          <FormattedMessage
            id='email_subscriptions.validation.email.invalid'
            defaultMessage='Enter a valid email address.'
          />
        </p>
      )}
    </div>
  );
};
