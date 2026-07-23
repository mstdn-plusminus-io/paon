import 'github-markdown-css';
import PropTypes from 'prop-types';
import { PureComponent } from 'react';

import { FormattedMessage, injectIntl } from 'react-intl';

import classnames from 'classnames';
import { Link } from 'react-router-dom';

import ImmutablePropTypes from 'react-immutable-proptypes';
import { connect } from 'react-redux';

import ReactMarkdown from 'react-markdown';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism';
import remarkGfm from 'remark-gfm';
import TurndownService from 'turndown';
import { gfm as turndownPluginGfm } from 'turndown-plugin-gfm';

import { Icon }  from 'mastodon/components/icon';
import PollContainer from 'mastodon/containers/poll_container';
import { autoPlayGif, languages as preloadedLanguages } from 'mastodon/initial_state';
import { decodeAme } from 'mastodon/utils/kaiwai';
import { komifloLinkify } from 'mastodon/utils/komiflo';
import { decodeMorse } from 'mastodon/utils/morse';

import { getStatusContent } from './status_content_helper';

export { getStatusContent } from './status_content_helper';

const codeFanceRegex = /<p>```(.*?)<br\/?>(.*?)```<\/p>/g;
const lineBreakRegex = /<br\/?>/g;
const languageRegex = /language-(\w+)/;
const searchRegex = /\[?(検索|Search)\]?$/;
const hashTagRegex = /\/tags\/(.*)$/;
const mentionRegex = /\/@(.*)$/;

const MAX_HEIGHT = 706; // 22px * 32 (+ 2px padding at the top)

const turndownService = new TurndownService({
  codeBlockStyle: 'fenced',
});
turndownService.escape = (content) => content;
turndownService.use(turndownPluginGfm);
turndownService.addRule('a', {
  filter: function (node, options) {
    return (
      options.linkStyle === 'inlined' &&
      node.nodeName === 'A' &&
      node.getAttribute('href')
    );
  },
  replacement: function(content, node) {
    if (node.classList.contains('hashtag')) {
      const match = node.href.match(hashTagRegex);
      if (match) {
        return `[#${decodeURIComponent(match[1])}](${node.href})`;
      }
    }
    if (node.classList.contains('mention')) {
      const match = node.href.match(mentionRegex);
      if (match) {
        return `[@${decodeURIComponent(match[1])}](${node.href})`;
      }
    }
    return content;
  },
});

class TranslateButton extends PureComponent {

  static propTypes = {
    translation: ImmutablePropTypes.map,
    onClick: PropTypes.func,
  };

  render () {
    const { translation, onClick } = this.props;

    if (translation) {
      const language     = preloadedLanguages.find(lang => lang[0] === translation.get('detected_source_language'));
      const languageName = language ? language[2] : translation.get('detected_source_language');
      const provider     = translation.get('provider');

      return (
        <div className='translate-button'>
          <div className='translate-button__meta'>
            <FormattedMessage id='status.translated_from_with' defaultMessage='Translated from {lang} using {provider}' values={{ lang: languageName, provider }} />
          </div>

          <button className='link-button' onClick={onClick}>
            <FormattedMessage id='status.show_original' defaultMessage='Show original' />
          </button>
        </div>
      );
    }

    return (
      <button className='status__content__translate-button' onClick={onClick}>
        <FormattedMessage id='status.translate' defaultMessage='Translate' />
      </button>
    );
  }

}

const mapStateToProps = state => ({
  languages: state.getIn(['server', 'translationLanguages', 'items']),
});

class StatusContent extends PureComponent {

  static contextTypes = {
    router: PropTypes.object,
    identity: PropTypes.object,
  };

  static propTypes = {
    status: ImmutablePropTypes.map.isRequired,
    statusContent: PropTypes.string,
    expanded: PropTypes.bool,
    onExpandedToggle: PropTypes.func,
    onTranslate: PropTypes.func,
    onClick: PropTypes.func,
    collapsible: PropTypes.bool,
    onCollapsedToggle: PropTypes.func,
    languages: ImmutablePropTypes.map,
    intl: PropTypes.object,
  };

  state = {
    hidden: true,
  };

  _updateStatusLinks () {
    const node = this.node;

    if (!node) {
      return;
    }

    const { status, onCollapsedToggle } = this.props;
    const links = node.querySelectorAll('a');

    let link, mention;

    for (var i = 0; i < links.length; ++i) {
      link = links[i];

      if (link.classList.contains('status-link')) {
        continue;
      }

      link.classList.add('status-link');

      mention = this.props.status.get('mentions').find(item => link.href === item.get('url'));

      if (mention) {
        link.addEventListener('click', this.onMentionClick.bind(this, mention), false);
        link.setAttribute('title', `@${mention.get('acct')}`);
        link.setAttribute('href', `/@${mention.get('acct')}`);
      } else if (link.textContent[0] === '#' || (link.previousSibling && link.previousSibling.textContent && link.previousSibling.textContent[link.previousSibling.textContent.length - 1] === '#')) {
        link.addEventListener('click', this.onHashtagClick.bind(this, link.text), false);
        link.setAttribute('href', `/tags/${link.text.replace(/^#/, '')}`);
      } else {
        link.setAttribute('title', link.href);
        link.setAttribute('target', '_blank');
        link.classList.add('unhandled-link');
      }
    }

    if (status.get('collapsed', null) === null && onCollapsedToggle) {
      const { collapsible, onClick } = this.props;

      const collapsed =
          collapsible
          && onClick
          && node.clientHeight > MAX_HEIGHT
          && status.get('spoiler_text').length === 0;

      onCollapsedToggle(collapsed);
    }
  }

  handleMouseEnter = ({ currentTarget }) => {
    if (autoPlayGif) {
      return;
    }

    const emojis = currentTarget.querySelectorAll('.custom-emoji');

    for (var i = 0; i < emojis.length; i++) {
      let emoji = emojis[i];
      emoji.src = emoji.getAttribute('data-original');
    }
  };

  handleMouseLeave = ({ currentTarget }) => {
    if (autoPlayGif) {
      return;
    }

    const emojis = currentTarget.querySelectorAll('.custom-emoji');

    for (var i = 0; i < emojis.length; i++) {
      let emoji = emojis[i];
      emoji.src = emoji.getAttribute('data-static');
    }
  };

  componentDidMount () {
    this._updateStatusLinks();
  }

  componentDidUpdate () {
    this._updateStatusLinks();
  }

  onMentionClick = (mention, e) => {
    if (this.context.router && e.button === 0 && !(e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      this.context.router.history.push(`/@${mention.get('acct')}`);
    }
  };

  onHashtagClick = (hashtag, e) => {
    hashtag = hashtag.replace(/^#/, '');

    if (this.context.router && e.button === 0 && !(e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      this.context.router.history.push(`/tags/${hashtag}`);
    }
  };

  handleMouseDown = (e) => {
    this.startXY = [e.clientX, e.clientY];
  };

  handleMouseUp = (e) => {
    if (!this.startXY) {
      return;
    }

    const [ startX, startY ] = this.startXY;
    const [ deltaX, deltaY ] = [Math.abs(e.clientX - startX), Math.abs(e.clientY - startY)];

    let element = e.target;
    while (element) {
      if (element.localName === 'button' || element.localName === 'a' || element.localName === 'label') {
        return;
      }
      element = element.parentNode;
    }

    if (deltaX + deltaY < 5 && e.button === 0 && this.props.onClick) {
      this.props.onClick();
    }

    this.startXY = null;
  };

  handleSpoilerClick = (e) => {
    e.preventDefault();

    if (this.props.onExpandedToggle) {
      // The parent manages the state
      this.props.onExpandedToggle();
    } else {
      this.setState({ hidden: !this.state.hidden });
    }
  };

  handleTranslate = () => {
    this.props.onTranslate();
  };

  setRef = (c) => {
    this.node = c;
  };

  onClickSearchButton = (keyword) => () => {
    window.open(`https://google.com/search?q=${keyword}`);
  }

  renderContent = (content) => {
    if (localStorage.plusminus_config_decode_morse === 'enabled') {
      if (content.__html.includes('－') || content.__html.includes('・') || content.__html.includes('.') || content.__html.includes('_')) {
        const el = document.createElement('div');
        el.innerHTML = content.__html;
        decodeMorse(el);
        content.__html = el.innerHTML;
      }
    }

    if (localStorage.plusminus_config_komiflo_linkify === 'enabled') {
      if (content.__html.includes('comics/')) {
        const el = document.createElement('div');
        el.innerHTML = content.__html;
        komifloLinkify(el);
        content.__html = el.innerHTML;
      }
    }

    if (localStorage.plusminus_config_decode_ame === 'enabled') {
      const el = document.createElement('div');
      el.innerHTML = content.__html;
      decodeAme(el);
      content.__html = el.innerHTML;
    }

    let isJumbomoji = false;
    if (localStorage.plusminus_config_jumbomoji === 'enabled') {
      const el = document.createElement('div');
      el.innerHTML = content.__html;
      if (el.innerText.trim() === '' && el.innerHTML.includes('title=":')) {
        const emojis = el.innerHTML.split('title=":').length - 1;
        if (emojis > 0 && emojis <= 23) {
          isJumbomoji = true;
        }
      }
    }

    let inner;

    if (localStorage.plusminus_config_content === 'markdown') {
      let html = `${content.__html}`;
      const codeFanceInners = html.matchAll(codeFanceRegex);
      for (const codeFanceInner of codeFanceInners) {
        const [orig, lang, body] = codeFanceInner;
        const element = document.createElement('div');
        element.innerHTML = body.replaceAll(lineBreakRegex, '\n').replaceAll(' ', '␚').replaceAll('</p><p>', '\n');
        html = html.replace(orig, `<pre><code${lang ? ` class="language-${lang}"` : ''}>${element.innerText}</code></pre>`);
      }

      const markdown = turndownService.turndown(html).replaceAll('␚', ' ');
      inner = (
        <ReactMarkdown
          className={`markdown-body ${isJumbomoji ? 'jumbomoji' : ''}`}
          remarkPlugins={[remarkGfm]}
          components={{
            br: () => '',
            // eslint-disable-next-line @typescript-eslint/no-unused-vars
            pre: ({ node, inline, className, children, ...props }) => {
              if (children[0].props?.node?.tagName === 'code') {
                if (children[0].props?.className?.includes('language')) {
                  return (
                    <pre className='prism'>
                      {children}
                    </pre>
                  );
                }
                return (
                  <pre className='code'>
                    {children}
                  </pre>
                );
              }
              return (
                <pre>
                  {children}
                </pre>
              );
            },
            code: ({ node, inline, className, children, ...props }) => {
              const match = languageRegex.exec(className || '');
              if (!inline && match) {
                return (
                  <SyntaxHighlighter
                    className='syntax-highlighter'
                    style={oneDark}
                    language={match[1]}
                    PreTag='div'
                    {...props}
                  >
                    {String(children).replace(/\n$/, '')}
                  </SyntaxHighlighter>
                );
              };
              return (
                <code className={className} {...props}>
                  {children}
                </code>
              );
            },
            // eslint-disable-next-line @typescript-eslint/no-unused-vars
            img: ({ node, inline, className, children, ...props }) => {
              const emojiClassName = [className];
              if (node.properties?.src?.includes('/emoji/')) {
                emojiClassName.push('emojione');
              }
              return (
                // eslint-disable-next-line jsx-a11y/alt-text
                <img className={emojiClassName.join(' ')} {...node.properties} />
              );
            },
          }}
        >
          {markdown}
        </ReactMarkdown>);
    } else {
      inner = <div className={isJumbomoji ? 'jumbomoji' : ''} dangerouslySetInnerHTML={content} />;
    }

    let searchBox = [];
    if (localStorage.plusminus_config_searchbox === 'visible' && (content.__html.includes('検索') || content.__html.includes('Search'))) {
      const element = document.createElement('div');
      element.innerHTML = content.__html.replaceAll('</p>', '␚').replaceAll(lineBreakRegex, '␚');
      element.innerText.replaceAll('␚', '\n').split('\n').forEach((line, index) => {
        if (line.match(searchRegex)) {
          const keyword = line.replace(searchRegex, '').trim();
          searchBox.push(
            <button key={index} className='plusminus-searchbox__container' onClick={this.onClickSearchButton(keyword)}>
              <input type='text' value={keyword} readOnly />
              <div>検索</div>
            </button>,
          );
        }
      });
    }
    return (
      <>
        {inner}
        {searchBox.length > 0 && (
          <div className='plusminus-searchbox'>
            {searchBox}
          </div>
        )}
      </>
    );
  }

  render () {
    const { status, intl, statusContent } = this.props;

    const hidden = this.props.onExpandedToggle ? !this.props.expanded : this.state.hidden;
    const renderReadMore = this.props.onClick && status.get('collapsed');
    const contentLocale = intl.locale.replace(/[_-].*/, '');
    const targetLanguages = this.props.languages?.get(status.get('language') || 'und');
    const renderTranslate = this.props.onTranslate && this.context.identity.signedIn && ['public', 'unlisted'].includes(status.get('visibility')) && status.get('search_index').trim().length > 0 && targetLanguages?.includes(contentLocale);

    const content = { __html: statusContent ?? getStatusContent(status) };
    const spoilerContent = { __html: status.getIn(['translation', 'spoilerHtml']) || status.get('spoilerHtml') };
    const language = status.getIn(['translation', 'language']) || status.get('language');
    const classNames = classnames('status__content', {
      'status__content--with-action': this.props.onClick && this.context.router,
      'status__content--with-spoiler': status.get('spoiler_text').length > 0,
      'status__content--collapsed': renderReadMore,
    });

    const readMoreButton = renderReadMore && (
      <button className='status__content__read-more-button' onClick={this.props.onClick} key='read-more'>
        <FormattedMessage id='status.read_more' defaultMessage='Read more' /><Icon id='angle-right' fixedWidth />
      </button>
    );

    const translateButton = renderTranslate && (
      <TranslateButton onClick={this.handleTranslate} translation={status.get('translation')} />
    );

    const poll = !!status.get('poll') && (
      <PollContainer pollId={status.get('poll')} lang={language} />
    );

    if (status.get('spoiler_text').length > 0) {
      let mentionsPlaceholder = '';

      const mentionLinks = status.get('mentions').map(item => (
        <Link to={`/@${item.get('acct')}`} key={item.get('id')} className='status-link mention'>
          @<span>{item.get('username')}</span>
        </Link>
      )).reduce((aggregate, item) => [...aggregate, item, ' '], []);

      const toggleText = hidden ? <FormattedMessage id='status.show_more' defaultMessage='Show more' /> : <FormattedMessage id='status.show_less' defaultMessage='Show less' />;

      if (hidden) {
        mentionsPlaceholder = <div>{mentionLinks}</div>;
      }

      return (
        <div className={classNames} ref={this.setRef} tabIndex={0} onMouseDown={this.handleMouseDown} onMouseUp={this.handleMouseUp} onMouseEnter={this.handleMouseEnter} onMouseLeave={this.handleMouseLeave}>
          <p style={{ marginBottom: hidden && status.get('mentions').isEmpty() ? '0px' : null }}>
            <span dangerouslySetInnerHTML={spoilerContent} className='translate' lang={language} />
            {' '}
            <button type='button' className={`status__content__spoiler-link ${hidden ? 'status__content__spoiler-link--show-more' : 'status__content__spoiler-link--show-less'}`} onClick={this.handleSpoilerClick} aria-expanded={!hidden}>{toggleText}</button>
          </p>

          {mentionsPlaceholder}

          <div tabIndex={!hidden ? 0 : null} className={`status__content__text ${!hidden ? 'status__content__text--visible' : ''} translate`} lang={language}>
            {this.renderContent(content)}
          </div>

          {!hidden && poll}
          {translateButton}
        </div>
      );
    } else if (this.props.onClick) {
      return (
        <>
          <div className={classNames} ref={this.setRef} tabIndex={0} key='status-content' onMouseEnter={this.handleMouseEnter} onMouseLeave={this.handleMouseLeave}>
            <div className='status__content__text status__content__text--visible translate' lang={language}>
              {this.renderContent(content)}
            </div>

            {poll}
            {translateButton}
          </div>

          {readMoreButton}
        </>
      );
    } else {
      return (
        <div className={classNames} ref={this.setRef} tabIndex={0} onMouseEnter={this.handleMouseEnter} onMouseLeave={this.handleMouseLeave}>
          <div className='status__content__text status__content__text--visible translate' lang={language}>
            {this.renderContent(content)}
          </div>

          {poll}
          {translateButton}
        </div>
      );
    }
  }

}

export default connect(mapStateToProps)(injectIntl(StatusContent));
