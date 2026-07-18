/* eslint-disable react/jsx-no-bind */

import PropTypes from 'prop-types';
import React from 'react';

import { injectIntl, FormattedMessage } from 'react-intl';

import Button from 'mastodon/components/button';
import PlusMinusSettingsSidebar from './plusminus_settings_sidebar';

import { open, download } from '../util/file';

const localStorageKeyPrefix = 'plusminus_config_';

// IndexedDB functions for plusminus settings
const readPlusminusSettings = (key) => {
  const indexedDB = window.indexedDB || window.mozIndexedDB || window.webkitIndexedDB || window.msIndexedDB;
  if (!indexedDB) return Promise.reject(new Error('IndexedDB not available'));

  return new Promise(function(resolve, reject) {
      const open = indexedDB.open('plusminus', 1);

      open.onerror = function() {
          reject(open.error);
      };

      open.onupgradeneeded = function() {
          const db = open.result;
          if (!db.objectStoreNames.contains('settings')) {
            db.createObjectStore('settings');
          }
      };

      open.onsuccess = function() {
          const db = open.result;
          const tx = db.transaction('settings', 'readonly');
          const store = tx.objectStore('settings');
          const getRequest = store.get(key);

          getRequest.onsuccess = function() {
              resolve(getRequest.result || []);
          };

          getRequest.onerror = function() {
              reject(getRequest.error);
          };
      };
  });
};

const writePlusminusSettings = (key, value) => {
  const indexedDB = window.indexedDB || window.mozIndexedDB || window.webkitIndexedDB || window.msIndexedDB;
  if (!indexedDB) return Promise.reject(new Error('IndexedDB not available'));

  return new Promise(function(resolve, reject) {
      const open = indexedDB.open('plusminus', 1);

      open.onerror = function() {
          reject(open.error);
      };

      open.onupgradeneeded = function() {
          const db = open.result;
          if (!db.objectStoreNames.contains('settings')) {
            db.createObjectStore('settings');
          }
      };

      open.onsuccess = function() {
          const db = open.result;
          const tx = db.transaction('settings', 'readwrite');
          const store = tx.objectStore('settings');
          const putRequest = store.put(value, key);

          putRequest.onsuccess = function() {
              resolve();
          };

          putRequest.onerror = function() {
              reject(putRequest.error);
          };
      };
  });
};

class PlusMinusSettingModalLoader extends React.Component {

  constructor() {
    super();

    this.state = {
      open: false,
    };

    this.onOpenEvent = this.onOpenEvent.bind(this);
  }

  componentDidMount() {
    window.__PLUS_MINUS_EVENTS__.addEventListener('openConfig', this.onOpenEvent);
  }

  componentWillUnmount() {
    window.__PLUS_MINUS_EVENTS__.removeEventListener('openConfig', this.onOpenEvent);
  }

  onOpenEvent() {
    this.setState({
      open: true,
    });
  }

  onClickCancel = () => {
    this.setState({ open: false });
  };

  render() {
    if (this.state.open) {
      return (
        <PlusMinusSettingModal onCancel={this.onClickCancel} />
      );
    }
    return <></>;
  }

}

const sections = [
  { id: 'mobile', title: 'スマートフォン向けUI', icon: 'mobile' },
  { id: 'timeline', title: 'タイムライン', icon: 'th-list' },
  { id: 'notifications', title: '通知', icon: 'bell' },
  { id: 'compose', title: '投稿欄', icon: 'pencil' },
  { id: 'import-export', title: 'インポート・エクスポート', icon: 'exchange' },
];

class PlusMinusSettingModal extends React.Component {

  static propTypes = {
    onCancel: PropTypes.func.isRequired,
  };

  UNSAFE_componentWillMount() {
    this.parseConfig(localStorage);
    this.loadNotificationSettings();
  }

  parseConfig(baseObj) {
    const currentSettings = Object.keys(baseObj).filter((key) => key.startsWith(localStorageKeyPrefix)).reduce((obj, key) => {
      if (baseObj[key].startsWith('{') || baseObj[key].startsWith('[')) {
        try {
          obj[key.replace(localStorageKeyPrefix, '')] = JSON.parse(baseObj[key]);
        } catch (e) {
          // eslint-disable-next-line eqeqeq
          if (baseObj[key] != null) {
            obj[key.replace(localStorageKeyPrefix, '')] = baseObj[key];
          }
        }
      } else {
        obj[key.replace(localStorageKeyPrefix, '')] = baseObj[key];
      }
      return obj;
    }, { ...this.state.config });
    this.setState({ config: currentSettings });
  }

  componentDidMount() {
    document.body.style.overflow = 'hidden';
    this.checkMobile();
    window.addEventListener('resize', this.checkMobile);
    if (this.state.isMobile) {
      this.mainRef = document.querySelector('.plusminus-settings__main');
      if (this.mainRef) {
        this.mainRef.addEventListener('scroll', this.handleScroll);
      }
    }
  }

  componentWillUnmount() {
    document.body.style.overflow = '';
    window.removeEventListener('resize', this.checkMobile);
    if (this.mainRef) {
      this.mainRef.removeEventListener('scroll', this.handleScroll);
    }
  }

  checkMobile = () => {
    const isMobile = window.innerWidth <= 895;
    if (this.state.isMobile !== isMobile) {
      this.setState({ isMobile });
      if (isMobile && !this.mainRef) {
        setTimeout(() => {
          this.mainRef = document.querySelector('.plusminus-settings__main');
          if (this.mainRef) {
            this.mainRef.addEventListener('scroll', this.handleScroll);
          }
        }, 0);
      } else if (!isMobile && this.mainRef) {
        this.mainRef.removeEventListener('scroll', this.handleScroll);
        this.mainRef = null;
      }
    }
  };

  handleScroll = () => {
    if (!this.state.isMobile || !this.mainRef) return;

    const scrollTop = this.mainRef.scrollTop;
    const sections = document.querySelectorAll('.plusminus-settings__section');
    let activeSection = 'mobile';

    sections.forEach((section) => {
      const rect = section.getBoundingClientRect();
      const sectionTop = rect.top - this.mainRef.getBoundingClientRect().top + scrollTop;

      // Check if the section is in the viewport (with 100px offset for better UX)
      if (sectionTop <= scrollTop + 100) {
        activeSection = section.id;
      }
    });

    if (this.state.activeSection !== activeSection) {
      this.setState({ activeSection });
    }
  };

  state = {
    developerModeButtonClicked: 0,
    notificationDenyList: [],
    activeSection: 'mobile',
    sidebarOpen: false,
    isMobile: window.innerWidth <= 895,
    config: {
      timestamp: 'relative',
      content: 'plain',
      sidenav: 'normal',
      post_button_location: 'normal',
      post_page_link: 'hidden',
      searchbox: 'hidden',
      custom_spoiler_button: 'hidden',
      custom_spoiler_buttons: ['そぎぎ'],
      sp_header: 'visible',
      decode_morse: 'disabled',
      encode_morse: 'disabled',
      reload_button: 'hidden',
      keyword_based_visibility: 'disabled',
      spoiler_keyword_based_visibility: 'disabled',
      keyword_based_visibilities: [{ keyword: 'ここだけの話なんだけど', visibility: 'unlisted' }],
      emotional_button: 'hidden',
      post_half_modal: 'disabled',
      quick_report: 'hidden',
      live_mode_button: 'hidden',
      developer_mode: 'disabled',
      decode_ame: 'disabled',
      encode_ame: 'disabled',
      komiflo_linkify: 'disabled',
      jumbomoji: 'disabled',
      filter_media_only_toots: 'disabled',
      max_image_scale: 200,
    },
  };

  updateConfig(key, value) {
    this.setState({ config: { ...this.state.config, [key]: value } });
  }

  updateNotificationDenyList = (newList) => {
    this.setState({ notificationDenyList: newList });
  };

  onClickDeveloperModeButton = () => {
    if (this.state.config.developer_mode === 'enabled') {
      return;
    }

    this.setState({
      developerModeButtonClicked: this.state.developerModeButtonClicked+1,
    }, () => {
      if (this.state.developerModeButtonClicked === 7) {
        localStorage[`${localStorageKeyPrefix}developer_mode`] = 'enabled';
        alert('開発者モードを有効化しました。リロードします。');
        location.reload();
      }
    });
  };

  loadNotificationSettings = async () => {
    try {
      const notificationDenyList = await readPlusminusSettings('notificationDenyList');
      this.setState({
        notificationDenyList: Array.isArray(notificationDenyList) ? notificationDenyList : [],
      });
    } catch (error) {
      console.warn('Failed to load notification settings from IndexedDB:', error);
      this.setState({
        notificationDenyList: [],
      });
    }
  };

  handleSectionChange = (sectionId) => {
    if (this.state.isMobile) {
      // スマホの場合はスムーズスクロール
      const element = document.getElementById(sectionId);
      if (element) {
        element.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    } else {
      // PCの場合はセクション切り替え
      this.setState({ activeSection: sectionId });
    }
  };

  toggleSidebar = () => {
    this.setState({ sidebarOpen: !this.state.sidebarOpen });
  };

  closeSidebar = () => {
    this.setState({ sidebarOpen: false });
  };

  renderMobileSection = (sectionId, title, content) => {
    return (
      <div id={sectionId} className='plusminus-settings__section' key={sectionId}>
        <h2 className='plusminus-settings__section-title'>{title}</h2>
        {content}
      </div>
    );
  };

  renderMobileUI = () => {
    return (
      <>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.sidenav === 'reverse'}
              onChange={(e) => this.updateConfig('sidenav', e.target.checked ? 'reverse' : 'normal')}
            />
            ナビゲージョンを左側に表示する
          </label>
          <p className='plusminus-settings__description'>サイド ナビゲーションを左側に表示します</p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.post_button_location === 'bottom_right'}
              onChange={(e) => this.updateConfig('post_button_location', e.target.checked ? 'bottom_right' : 'normal')}
            />
            投稿ボタンを右下に表示する
          </label>
          <p className='plusminus-settings__description'>画面上部の投稿ボタンを右下に表示します</p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.post_half_modal === 'enabled'}
              onChange={(e) => this.updateConfig('post_half_modal', e.target.checked ? 'enabled' : 'disabled')}
            />
            投稿欄をハーフモーダルで表示する
          </label>
          <p className='plusminus-settings__description'>投稿欄を単一の画面で表示せずに、画面下部にハーフモーダルで表示します</p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.sp_header === 'hidden'}
              onChange={(e) => this.updateConfig('sp_header', e.target.checked ? 'hidden' : 'visible')}
            />
            ヘッダーを非表示にする
          </label>
          <p className='plusminus-settings__description'>画面上部のヘッダーを非表示にします</p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.reload_button === 'visible'}
              onChange={(e) => this.updateConfig('reload_button', e.target.checked ? 'visible' : 'hidden')}
            />
            リロードボタンを表示する
          </label>
          <p className='plusminus-settings__description'>サイド ナビゲーション最上部にリロードボタンを表示します<br />PWAとしてインストールしている場合にリロードできない問題を暫定的に解決します</p>
        </div>
      </>
    );
  };

  renderTimeline = () => {
    return (
      <>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.timestamp === 'absolute'}
              onChange={(e) => this.updateConfig('timestamp', e.target.checked ? 'absolute' : 'relative')}
            />
            絶対時刻表示
          </label>
          <p className='plusminus-settings__description'>タイムライン中の時刻表示を相対時刻から絶対時刻に変更します</p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.content === 'markdown'}
              onChange={(e) => this.updateConfig('content', e.target.checked ? 'markdown' : 'plain')}
            />
            Markdownをレンダリング（実験的）
          </label>
          <p className='plusminus-settings__description'><a className='plusminus-settings__link' href='https://github.com/mixmark-io/turndown' target='_blank'>turndown</a>と<a className='plusminus-settings__link' href='https://github.com/remarkjs/react-markdown' target='_blank'>react-markdown</a>を使用してMarkdownをレンダリングします<br />投稿のHTMLに依存するため、スペースや改行などが正しく反映されるとは限りません</p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.jumbomoji === 'enabled'}
              onChange={(e) => this.updateConfig('jumbomoji', e.target.checked ? 'enabled' : 'disabled')}
            />
            Jumbomojiを有効にする
          </label>
          <p className='plusminus-settings__description'>Slackのように、絵文字のみの投稿の場合は絵文字を大きく表示します</p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.post_page_link === 'visible'}
              onChange={(e) => this.updateConfig('post_page_link', e.target.checked ? 'visible' : 'hidden')}
            />
            投稿元ページのリンクを表示する
          </label>
          <p className='plusminus-settings__description'>投稿時刻の右側に投稿元ページを別タブで開くリンクを追加します</p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.searchbox === 'visible'}
              onChange={(e) => this.updateConfig('searchbox', e.target.checked ? 'visible' : 'hidden')}
            />
            Misskey Flavored Markdownの検索窓を展開する
          </label>
          <p className='plusminus-settings__description'><a className='plusminus-settings__link' href='https://wiki.misskey.io/ja/function/mfm#%E6%A4%9C%E7%B4%A2%E7%AA%93' target='_blank'>Misskey Flavored Markdownの検索窓</a>を投稿本文の下に展開します</p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.decode_morse === 'enabled'}
              onChange={(e) => this.updateConfig('decode_morse', e.target.checked ? 'enabled' : 'disabled')}
            />
            y4aスタイルのモールス符号をデコードする
          </label>
          <p className='plusminus-settings__description'>
            <a className='plusminus-settings__link' href='https://github.com/shibafu528/Yukari' target='_blank'>Yukari for Android</a>スタイルの日本語モールス符号をカタカナに変換して表示します<br />
            英数モールス符号もデコードできますが、互換性はありません
          </p>
        </div>
        {this.state.config.developer_mode === 'enabled' && <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.komiflo_linkify === 'enabled'}
              onChange={(e) => this.updateConfig('komiflo_linkify', e.target.checked ? 'enabled' : 'disabled')}
            />
            comics/xxxxxx の文字列をKomifloのリンクに置き換える
          </label>
        </div>}
        {this.state.config.developer_mode === 'enabled' && <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.decode_ame === 'enabled'}
              onChange={(e) => this.updateConfig('decode_ame', e.target.checked ? 'enabled' : 'disabled')}
            />
            ᕂᕙᕸᕵᖋᕂᖁᕸᖓᕋᖓᖒᕧᕓーᕩᕙᖋ
          </label>
        </div>}
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.quick_report === 'visible'}
              onChange={(e) => this.updateConfig('quick_report', e.target.checked ? 'visible' : 'hidden')}
            />
            投稿下部のアクションボタンに通報ボタンを追加する
          </label>
          <p className='plusminus-settings__description'>
            通報をすばやく、簡単に行えるようになります
          </p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.filter_media_only_toots === 'enabled'}
              onChange={(e) => this.updateConfig('filter_media_only_toots', e.target.checked ? 'enabled' : 'disabled')}
            />
            <code>🖼️</code> から始まる名前のリストのトゥートをメディアでフィルタする
          </label>
          <p className='plusminus-settings__description'>
            名前が <code>🖼️</code> から始まるリストTLの表示対象を、メディアが添付されているものだけに絞り込みます
          </p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            画像プレビューの拡大倍率制限:&nbsp;
            <input
              type='number'
              value={this.state.config.max_image_scale}
              onChange={(e) => {
                const value = parseInt(e.target.value || '0');
                if (!isNaN(value)) {
                  this.updateConfig('max_image_scale', value);
                }
              }}
            />
            %
          </label>
        </div>
      </>
    );
  };

  renderNotifications = () => {
    return (
      <>
        <div className='plusminus-settings__config'>
          <label>
            通知を表示しないアカウント
          </label>
          <p className='plusminus-settings__description'>
            指定したアカウントからのブラウザ通知を表示しません。<br />
            通知タイムラインには表示されます。<br />
            アカウント名は @username@example.com の形式で入力してください。
          </p>

          <div className='plusminus-settings__input-list'>
            {this.state.notificationDenyList?.map((acct, index) => (
              <div key={`${index}_${this.state.notificationDenyList.length}`} className='plusminus-settings__input-item'>
                <input
                  className='plusminus-settings__input-text'
                  type='text'
                  placeholder='@username@example.com'
                  value={acct}
                  onChange={(e) => {
                    const newList = [...this.state.notificationDenyList];
                    newList[index] = e.target.value;
                    this.updateNotificationDenyList(newList);
                  }}
                />
                <button
                  className='plusminus-settings__input-button'
                  onClick={() => {
                    const newList = [...this.state.notificationDenyList];
                    newList.splice(index, 1);
                    this.updateNotificationDenyList(newList);
                  }}
                >
                  -
                </button>
              </div>
            ))}
            <button
              className='plusminus-settings__input-add-button'
              onClick={() => {
                const newList = [...this.state.notificationDenyList];
                newList.push('');
                this.updateNotificationDenyList(newList);
              }}
            >
              +
            </button>
          </div>
        </div>
      </>
    );
  };

  renderCompose = () => {
    return (
      <>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.custom_spoiler_button === 'visible'}
              onChange={(e) => this.updateConfig('custom_spoiler_button', e.target.checked ? 'visible' : 'hidden')}
            />
            CW (Content Warning)のプリセットボタンを表示する
          </label>
          <p className='plusminus-settings__description'>ボタンを押すだけで任意の文章をCWに入力できるようになります<br />また、CWが有効になっていない場合は自動的に有効になります</p>

          <div className='plusminus-settings__input-list'>
            {this.state.config.custom_spoiler_buttons?.map((buttonText, index) => (
              <div key={`${index}_${this.state.config.custom_spoiler_buttons.length}`} className='plusminus-settings__input-item'>
                <input
                  className='plusminus-settings__input-text'
                  type='text'
                  value={buttonText}
                  onChange={(e) => {
                    this.state.config.custom_spoiler_buttons[index] = e.target.value;
                    this.updateConfig('custom_spoiler_buttons', this.state.config.custom_spoiler_buttons);
                  }}
                />
                <button
                  className='plusminus-settings__input-button'
                  onClick={() => {
                    this.state.config.custom_spoiler_buttons.splice(index, 1);
                    this.updateConfig('custom_spoiler_buttons', this.state.config.custom_spoiler_buttons);
                  }}
                >
                  -
                </button>
              </div>
            ))}
            <button
              className='plusminus-settings__input-add-button'
              onClick={() => {
                this.state.config.custom_spoiler_buttons.push('');
                this.updateConfig('custom_spoiler_buttons', this.state.config.custom_spoiler_buttons);
              }}
            >
              +
            </button>
          </div>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.keyword_based_visibility === 'enabled'}
              onChange={(e) => this.updateConfig('keyword_based_visibility', e.target.checked ? 'enabled' : 'disabled')}
            />
            キーワードで公開範囲を自動的に設定する
          </label>
          <p className='plusminus-settings__description'>指定した文字列が本文に含まれる場合に、公開範囲を自動的に設定します</p>

          <div className='plusminus-settings__config plusminus-settings__config--child'>
            <label>
              <input
                type='checkbox'
                checked={this.state.config.spoiler_keyword_based_visibility === 'enabled'}
                onChange={(e) => this.updateConfig('spoiler_keyword_based_visibility', e.target.checked ? 'enabled' : 'disabled')}
              />
              CW (Content Warning)も対象にする
            </label>
            <p className='plusminus-settings__description'>
              指定した文字列がCWに含まれる場合にも、公開範囲を自動的に設定します<br />
              CWプリセットボタンで有用です
            </p>
          </div>

          <div className='plusminus-settings__config'>
            <p className='plusminus-settings__description'>キーワードは上にあるものが優先されます</p>
            <div className='plusminus-settings__input-list'>
              {this.state.config.keyword_based_visibilities?.map(({ keyword, visibility }, index) => (
                <div key={`${index}_${this.state.config.custom_spoiler_buttons.length}`} className='plusminus-settings__input-item'>
                  <div className='plusminus-settings__input-wrapper'>
                    <input
                      className='plusminus-settings__input-text'
                      type='text'
                      placeholder={'キーワード'}
                      value={keyword}
                      onChange={(e) => {
                        this.state.config.keyword_based_visibilities[index].keyword = e.target.value;
                        this.updateConfig('keyword_based_visibilities', this.state.config.keyword_based_visibilities);
                      }}
                    />
                    <select
                      className='plusminus-settings__input-select'
                      value={visibility}
                      onChange={(e) => {
                        this.state.config.keyword_based_visibilities[index].visibility = e.target.value;
                        this.updateConfig('keyword_based_visibilities', this.state.config.keyword_based_visibilities);
                      }}
                    >
                      <option value='public'>Public</option>
                      <option value='unlisted'>Unlisted</option>
                      <option value='private'>Followers only</option>
                      <option value='direct'>Mentioned people only</option>
                    </select>
                  </div>
                  <div className='plusminus-settings__input-button-wrapper'>
                    <div className='plusminus-settings__input-order-buttons'>
                      <button
                        className='plusminus-settings__input-button'
                        disabled={index === 0}
                        onClick={() => {
                          const obj = this.state.config.keyword_based_visibilities[index];
                          this.state.config.keyword_based_visibilities.splice(index, 1);
                          this.state.config.keyword_based_visibilities.splice(index - 1, 0, obj);
                          this.updateConfig('keyword_based_visibilities', this.state.config.keyword_based_visibilities);
                        }}
                      >
                        ↑
                      </button>
                      <button
                        className='plusminus-settings__input-button'
                        disabled={index === this.state.config.keyword_based_visibilities.length - 1}
                        onClick={() => {
                          const obj = this.state.config.keyword_based_visibilities[index];
                          this.state.config.keyword_based_visibilities.splice(index, 1);
                          this.state.config.keyword_based_visibilities.splice(index + 1, 0, obj);
                          this.updateConfig('keyword_based_visibilities', this.state.config.keyword_based_visibilities);
                        }}
                      >
                        ↓
                      </button>
                    </div>

                    <button
                      className='plusminus-settings__input-button'
                      onClick={() => {
                        this.state.config.keyword_based_visibilities.splice(index, 1);
                        this.updateConfig('keyword_based_visibilities', this.state.config.keyword_based_visibilities);
                      }}
                    >
                      -
                    </button>
                  </div>
                </div>
              ))}
              <button
                className='plusminus-settings__input-add-button'
                onClick={() => {
                  this.state.config.keyword_based_visibilities.push({ keyword: '', visibility: 'public' });
                  this.updateConfig('keyword_based_visibilities', this.state.config.keyword_based_visibilities);
                }}
              >
                +
              </button>
            </div>
          </div>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.emotional_button === 'visible'}
              onChange={(e) => this.updateConfig('emotional_button', e.target.checked ? 'visible' : 'hidden')}
            />
            半角英数をUnicodeの数学用英数字ブロックに置き換えるボタンを表示する
          </label>
          <p className='plusminus-settings__description'>
            <code>Lorem ipsum dolor sit amet,</code> を <code>𝓛𝓸𝓻𝓮𝓶 𝓲𝓹𝓼𝓾𝓶 𝓭𝓸𝓵𝓸𝓻 𝓼𝓲𝓽 𝓪𝓶𝓮𝓽,</code> などのエモい文字に置き換えるボタンを表示します
          </p>
        </div>
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.encode_morse === 'enabled'}
              onChange={(e) => this.updateConfig('encode_morse', e.target.checked ? 'enabled' : 'disabled')}
            />
            y4aスタイルのモールス符号にエンコードするボタンを表示する
          </label>
          <p className='plusminus-settings__description'>
            ひらがな/カタカナを<a className='plusminus-settings__link' href='https://github.com/shibafu528/Yukari' target='_blank'>Yukari for Android</a>スタイルのモールス符号に変換します<br />
            英数モールス符号もエンコードできますが、互換性はありません<br />
            漢字/一部を除く記号は対象外です
          </p>
        </div>
        {this.state.config.developer_mode === 'enabled' && <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.encode_ame === 'enabled'}
              onChange={(e) => this.updateConfig('encode_ame', e.target.checked ? 'enabled' : 'disabled')}
            />
            ᕂᕙᕸᕵᖋᕂᖁᕸᖓᕋᖓᖒᕊᕓᕪᕆᕼᕟᖓᖒᕲᖇᕆᕘᕙᖋ
          </label>
        </div>}
        <div className='plusminus-settings__config'>
          <label>
            <input
              type='checkbox'
              checked={this.state.config.live_mode_button === 'visible'}
              onChange={(e) => this.updateConfig('live_mode_button', e.target.checked ? 'visible' : 'hidden')}
            />
            実況モードを切り替えるボタンを表示する
          </label>
          <p className='plusminus-settings__description'>
            投稿後もハッシュタグを投稿欄に残すことで、ハッシュタグを使った実況が行いやすくなります<br />
            この設定を無効化すると、実況モードも自動的に無効化されます
          </p>
        </div>
      </>
    );
  };

  renderImportExport = () => {
    return (
      <>
        <div className='plusminus-settings__config'>
          <h2>設定のエクスポート</h2>
          <p className='plusminus-settings__description'>
            現在の設定をJSONファイルとしてダウンロードします。<br />
            他のブラウザや端末で設定を復元する際に使用できます。
          </p>
          <Button className='plusminus-settings__import-export-button' onClick={this.handleExport}>設定をエクスポート</Button>
        </div>

        <div className='plusminus-settings__config'>
          <h2>設定のインポート</h2>
          <p className='plusminus-settings__description'>
            エクスポートしたJSONファイルから設定を読み込みます。<br />
            現在の設定は上書きされますのでご注意ください。
          </p>
          <Button className='plusminus-settings__import-export-button' onClick={this.handleImport}>設定をインポート</Button>
        </div>
      </>
    );
  };

  renderContent = () => {
    switch (this.state.activeSection) {
      case 'mobile':
        return this.renderMobileUI();
      case 'timeline':
        return this.renderTimeline();
      case 'notifications':
        return this.renderNotifications();
      case 'compose':
        return this.renderCompose();
      case 'import-export':
        return this.renderImportExport();
      default:
        return null;
    }
  };

  render() {
    const { isMobile } = this.state;

    return (
      <div className={'plusminus-settings__root'}>
        {isMobile && (
          <button
            className='plusminus-settings__hamburger'
            onClick={this.toggleSidebar}
            aria-label='メニューを開く'
          >
            <i className='fa fa-bars' />
          </button>
        )}
        {isMobile && (
          <div className='plusminus-settings__header'>
            <h1 className='plusminus-settings__title plusminus-settings__title--mobile'>
              Pa<button className='plusminus-settings__title plusminus-settings__developer-mode-button' onClick={this.onClickDeveloperModeButton}>o</button>n設定{this.state.config.developer_mode === 'enabled' && '!'}
            </h1>
          </div>
        )}

        <PlusMinusSettingsSidebar
          sections={sections}
          activeSection={this.state.activeSection}
          onSectionChange={this.handleSectionChange}
          isMobile={isMobile}
          isOpen={this.state.sidebarOpen}
          onClose={this.closeSidebar}
        />

        <div className='plusminus-settings__container'>
          {!isMobile && (
            <h1 className='plusminus-settings__title'>
              Pa<button className='plusminus-settings__title plusminus-settings__developer-mode-button' onClick={this.onClickDeveloperModeButton}>o</button>n設定{this.state.config.developer_mode === 'enabled' && '!'}
            </h1>
          )}

          <div className={`plusminus-settings__main ${isMobile ? 'plusminus-settings__main--mobile' : ''}`}>
            <p className='plusminus-settings__hint'>以下の設定はブラウザごとに保存されます</p>
            <hr />

            {isMobile ? (
              // スマホでは全セクションを表示
              <div className='plusminus-settings__mobile-content'>
                {this.renderMobileSection('mobile', 'スマートフォン向けUI', this.renderMobileUI())}
                {this.renderMobileSection('timeline', 'タイムライン', this.renderTimeline())}
                {this.renderMobileSection('notifications', '通知', this.renderNotifications())}
                {this.renderMobileSection('compose', '投稿欄', this.renderCompose())}
                {this.renderMobileSection('import-export', 'インポート・エクスポート', this.renderImportExport())}
              </div>
            ) : (
              // PCでは選択されたセクションのみ表示
              <div className='plusminus-settings__content'>
                {this.renderContent()}
              </div>
            )}
          </div>
        </div>

        <div className='plusminus-settings__action-bar'>
          <div className='plusminus-settings__cancel-button'>
            <Button onClick={this.handleCancel} className='button-secondary'>
              <FormattedMessage id='confirmation_modal.cancel' defaultMessage='Cancel' />
            </Button>
          </div>
          <Button onClick={this.handleSave}>
            <FormattedMessage id='compose_form.save_changes' defaultMessage='Save' />
          </Button>
        </div>
      </div>
    );
  }

  handleCancel = () => {
    this.props.onCancel();
  };

  convert = (obj = {}) => {
    Object.keys(this.state.config).forEach((key) =>
      obj[`${localStorageKeyPrefix}${key}`] = typeof this.state.config[key] === 'object' ? JSON.stringify(this.state.config[key]) : this.state.config[key],
    );
    return obj;
  };

  handleImport = async () => {
    const text = await open('.json');
    if (!text) {
      return;
    }

    try {
      const config = JSON.parse(text);
      this.parseConfig(config);
    } catch (e) {
      console.error(e);
      alert('JSONのパースに失敗しました');
    }
  };

  handleExport = () => {
    const config = JSON.stringify(this.convert());
    download(`mastodon-plusminus-settings-${new Date().getTime()}.json`, config);
  };

  handleSave = async () => {
    Object.keys(this.state.config).forEach((key) =>
      localStorage[`${localStorageKeyPrefix}${key}`] = typeof this.state.config[key] === 'object' ? JSON.stringify(this.state.config[key]) : this.state.config[key],
    );

    if (this.state.config.live_mode_button === 'hidden') {
      // NOTE: 実況モードボタンが無効化されているので、実況モードも無効化する
      localStorage[`${localStorageKeyPrefix}live_mode`] = 'disabled';
    }

    // Save notification settings to IndexedDB
    try {
      await writePlusminusSettings('notificationDenyList', this.state.notificationDenyList.filter(acct => acct.trim() !== ''));
      localStorage[`${localStorageKeyPrefix}notification_deny_list`] = JSON.stringify(this.state.notificationDenyList.filter(acct => acct.trim() !== ''));
    } catch (error) {
      console.error('Failed to save notification settings to IndexedDB:', error);
      alert('通知設定の保存に失敗しました。');
      return;
    }

    location.reload();
  };

}

export default injectIntl(PlusMinusSettingModalLoader);
