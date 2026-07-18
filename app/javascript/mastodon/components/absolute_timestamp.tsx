import PropTypes from 'prop-types';
import { Component } from 'react';

import { injectIntl } from 'react-intl';
import type { IntlShape } from 'react-intl';

const dateFormatOptions = {
  hour12: false,
  year: 'numeric',
  month: 'short',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
} as const;

interface Props {
  intl: IntlShape;
  timestamp: string;
}
interface States {
  now: number;
}

// eslint-disable-next-line react/prefer-stateless-function
class AbsoluteTimestamp extends Component<Props, States> {
  static propTypes = {
    intl: PropTypes.object.isRequired,
    timestamp: PropTypes.string.isRequired,
  };

  render() {
    const { timestamp, intl } = this.props;

    const date = new Date(timestamp);
    const now = new Date();
    let timeString = date.toLocaleTimeString();
    if (date.toLocaleDateString() !== now.toLocaleDateString()) {
      timeString = `${date.toLocaleDateString()} ${timeString}`;
    }

    return (
      <time
        dateTime={timestamp}
        title={intl.formatDate(date, dateFormatOptions)}
      >
        {timeString}
      </time>
    );
  }
}

const AbsoluteTimestampWithIntl = injectIntl(
  AbsoluteTimestamp as React.ComponentType<Props>,
);

export { AbsoluteTimestampWithIntl as AbsoluteTimestamp };
