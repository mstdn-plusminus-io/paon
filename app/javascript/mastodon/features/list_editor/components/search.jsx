import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { defineMessages, injectIntl } from 'react-intl';

import classNames from 'classnames';

import { connect } from 'react-redux';

import { Icon }  from 'mastodon/components/icon';

import { fetchListSuggestions, clearListSuggestions, changeListSuggestions } from '../../../actions/lists';

const messages = defineMessages({
  search: { id: 'lists.search', defaultMessage: 'Search' },
  clear: { id: 'lists.search_clear', defaultMessage: 'Clear search' },
});

const mapStateToProps = state => ({
  value: state.getIn(['listEditor', 'suggestions', 'value']),
  isLoading: state.getIn(['listEditor', 'suggestions', 'isLoading']),
});

const mapDispatchToProps = dispatch => ({
  onSubmit: value => dispatch(fetchListSuggestions(value)),
  onClear: () => dispatch(clearListSuggestions()),
  onChange: value => dispatch(changeListSuggestions(value)),
});

class Search extends PureComponent {

  static propTypes = {
    intl: PropTypes.object.isRequired,
    value: PropTypes.string.isRequired,
    onChange: PropTypes.func.isRequired,
    onSubmit: PropTypes.func.isRequired,
    onClear: PropTypes.func.isRequired,
    isLoading: PropTypes.bool.isRequired,
  };

  handleChange = e => {
    this.props.onChange(e.target.value);
  };

  handleKeyDown = e => {
    if (e.key === 'Enter') {
      e.preventDefault();
      this.props.onSubmit(this.props.value);
    }
  };

  handleClear = () => {
    this.props.onClear();
  };

  render () {
    const { value, intl, isLoading } = this.props;
    const hasValue = value.length > 0;

    return (
      <div className='list-editor__search search'>
        <label>
          <span style={{ display: 'none' }}>{intl.formatMessage(messages.search)}</span>

          <input
            className='search__input'
            type='text'
            value={value}
            onChange={this.handleChange}
            onKeyDown={this.handleKeyDown}
            placeholder={intl.formatMessage(messages.search)}
            aria-label={intl.formatMessage(messages.search)}
            aria-busy={isLoading}
          />
        </label>

        <button type='button' className='search__icon' onClick={this.handleClear} disabled={!hasValue} aria-label={intl.formatMessage(messages.clear)}>
          <Icon id='search' className={classNames({ active: !hasValue })} />
          <Icon id='times-circle' className={classNames({ active: hasValue })} />
        </button>
      </div>
    );
  }

}

export default connect(mapStateToProps, mapDispatchToProps)(injectIntl(Search));
