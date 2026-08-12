import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { defineMessages, injectIntl } from 'react-intl';

import classNames from 'classnames';

import ImmutablePropTypes from 'react-immutable-proptypes';
import ImmutablePureComponent from 'react-immutable-pure-component';

import CancelIcon from '@/material-icons/400-24px/cancel.svg?react';
import AutosuggestInput from 'mastodon/components/autosuggest_input';
import { IconButton } from 'mastodon/components/icon_button';

const messages = defineMessages({
  option_placeholder: { id: 'compose_form.poll.option_placeholder', defaultMessage: 'Choice {number}' },
  remove_option: { id: 'compose_form.poll.remove_option', defaultMessage: 'Remove this choice' },
  poll_duration: { id: 'compose_form.poll.duration', defaultMessage: 'Poll length' },
  type: { id: 'compose_form.poll.type', defaultMessage: 'Style' },
  singleChoice: { id: 'compose_form.poll.single', defaultMessage: 'Single choice' },
  multipleChoice: { id: 'compose_form.poll.multiple', defaultMessage: 'Multiple choice' },
  switchToMultiple: { id: 'compose_form.poll.switch_to_multiple', defaultMessage: 'Change poll to allow multiple choices' },
  switchToSingle: { id: 'compose_form.poll.switch_to_single', defaultMessage: 'Change poll to allow for a single choice' },
  minutes: { id: 'intervals.full.minutes', defaultMessage: '{number, plural, one {# minute} other {# minutes}}' },
  hours: { id: 'intervals.full.hours', defaultMessage: '{number, plural, one {# hour} other {# hours}}' },
  days: { id: 'intervals.full.days', defaultMessage: '{number, plural, one {# day} other {# days}}' },
});

class OptionIntl extends PureComponent {

  static propTypes = {
    title: PropTypes.string.isRequired,
    lang: PropTypes.string,
    index: PropTypes.number.isRequired,
    isPollMultiple: PropTypes.bool,
    autoFocus: PropTypes.bool,
    maxCharacters: PropTypes.number.isRequired,
    maxOptions: PropTypes.number.isRequired,
    onChange: PropTypes.func.isRequired,
    onRemove: PropTypes.func.isRequired,
    suggestions: ImmutablePropTypes.list,
    onClearSuggestions: PropTypes.func.isRequired,
    onFetchSuggestions: PropTypes.func.isRequired,
    onSuggestionSelected: PropTypes.func.isRequired,
    intl: PropTypes.object.isRequired,
  };

  handleOptionTitleChange = e => {
    this.props.onChange(this.props.index, e.target.value, this.props.maxOptions);
  };

  handleOptionRemove = () => {
    this.props.onRemove(this.props.index);
  };

  onSuggestionsClearRequested = () => {
    this.props.onClearSuggestions();
  };

  onSuggestionsFetchRequested = (token) => {
    this.props.onFetchSuggestions(token);
  };

  onSuggestionSelected = (tokenStart, token, value) => {
    this.props.onSuggestionSelected(tokenStart, token, value, ['poll', 'options', this.props.index]);
  };

  render () {
    const { isPollMultiple, title, lang, index, autoFocus, intl, maxCharacters } = this.props;

    return (
      <li>
        <label className={classNames('poll__option editable', { empty: title.trim().length === 0 })}>
          <span className={classNames('poll__input', { checkbox: isPollMultiple })} aria-hidden='true' />

          <AutosuggestInput
            placeholder={intl.formatMessage(messages.option_placeholder, { number: index + 1 })}
            maxLength={maxCharacters}
            value={title}
            lang={lang}
            spellCheck
            onChange={this.handleOptionTitleChange}
            suggestions={this.props.suggestions}
            onSuggestionsFetchRequested={this.onSuggestionsFetchRequested}
            onSuggestionsClearRequested={this.onSuggestionsClearRequested}
            onSuggestionSelected={this.onSuggestionSelected}
            searchTokens={[':']}
            autoFocus={autoFocus}
          />
        </label>

        <div className='poll__cancel'>
          <IconButton disabled={index <= 1 || title.trim().length === 0} title={intl.formatMessage(messages.remove_option)} icon='times' iconComponent={CancelIcon} onClick={this.handleOptionRemove} />
        </div>
      </li>
    );
  }

}

const Option = injectIntl(OptionIntl);

class PollForm extends ImmutablePureComponent {

  static propTypes = {
    options: ImmutablePropTypes.list,
    lang: PropTypes.string,
    expiresIn: PropTypes.number,
    isMultiple: PropTypes.bool,
    onChangeOption: PropTypes.func.isRequired,
    onRemoveOption: PropTypes.func.isRequired,
    onChangeSettings: PropTypes.func.isRequired,
    suggestions: ImmutablePropTypes.list,
    onClearSuggestions: PropTypes.func.isRequired,
    onFetchSuggestions: PropTypes.func.isRequired,
    onSuggestionSelected: PropTypes.func.isRequired,
    intl: PropTypes.object.isRequired,
    maxOptions: PropTypes.number.isRequired,
    maxCharacters: PropTypes.number.isRequired,
  };

  handleSelectDuration = e => {
    this.props.onChangeSettings(e.target.value, this.props.isMultiple);
  };

  handleSelectType = e => {
    this.props.onChangeSettings(this.props.expiresIn, e.target.value === 'true');
  };

  render () {
    const { options, lang, expiresIn, isMultiple, onChangeOption, onRemoveOption, intl, maxOptions, maxCharacters, ...other } = this.props;

    if (!options) {
      return null;
    }

    const autoFocusIndex = options.indexOf('');

    return (
      <div className='compose-form__poll-wrapper'>
        <ul>
          {options.map((title, i) => <Option title={title} lang={lang} key={i} index={i} onChange={onChangeOption} onRemove={onRemoveOption} isPollMultiple={isMultiple} maxOptions={maxOptions} maxCharacters={maxCharacters} autoFocus={i === autoFocusIndex} {...other} />)}
        </ul>

        <div className='poll__footer'>
          <label className='poll__footer__select'>
            <span>{intl.formatMessage(messages.poll_duration)}</span>
            {/* eslint-disable-next-line jsx-a11y/no-onchange */}
            <select value={expiresIn} onChange={this.handleSelectDuration}>
              <option value={300}>{intl.formatMessage(messages.minutes, { number: 5 })}</option>
              <option value={1800}>{intl.formatMessage(messages.minutes, { number: 30 })}</option>
              <option value={3600}>{intl.formatMessage(messages.hours, { number: 1 })}</option>
              <option value={21600}>{intl.formatMessage(messages.hours, { number: 6 })}</option>
              <option value={43200}>{intl.formatMessage(messages.hours, { number: 12 })}</option>
              <option value={86400}>{intl.formatMessage(messages.days, { number: 1 })}</option>
              <option value={259200}>{intl.formatMessage(messages.days, { number: 3 })}</option>
              <option value={604800}>{intl.formatMessage(messages.days, { number: 7 })}</option>
            </select>
          </label>

          <label className='poll__footer__select'>
            <span>{intl.formatMessage(messages.type)}</span>
            {/* eslint-disable-next-line jsx-a11y/no-onchange */}
            <select value={String(isMultiple)} onChange={this.handleSelectType}>
              <option value='false'>{intl.formatMessage(messages.singleChoice)}</option>
              <option value='true'>{intl.formatMessage(messages.multipleChoice)}</option>
            </select>
          </label>
        </div>
      </div>
    );
  }

}

export default injectIntl(PollForm);
